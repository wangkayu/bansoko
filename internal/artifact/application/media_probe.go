package application

import (
	"context"
	"errors"
	"time"

	"bansoko/internal/download/domain"
	observabilityport "bansoko/internal/observability/port"
)

const (
	// mediaProbeBudget bounds the total time one inspection spends reading
	// media facts, so a season pack cannot spend the whole attempt window on
	// probing. Files left over when it runs out keep empty facts.
	mediaProbeBudget = 60 * time.Second

	mediaProbeComponent = "artifact"
	mediaProbeOperation = "media_probe"
)

// probeMediaFacts reads the execution-time media facts of every video file
// into targets[i].media. It is best effort by contract: a missing tool, a
// timeout, a failure or an empty answer leaves that file's facts empty and
// never fails the inspection. Naming then renders the empty facts per the
// empty-value rules (NAMING_FACT_MISSING only when the path becomes invalid).
// Preview is unaffected: it never reads this data.
func (s *Service) probeMediaFacts(ctx context.Context, artifactID string, targets []artifactInspectionTarget, inspected []domain.ArtifactFacts) {
	if s == nil || s.prober == nil {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, mediaProbeBudget)
	defer cancel()

	for index, file := range inspected {
		if normalizeMediaKind(file.MediaKind, file.Path) != "video" {
			continue
		}
		if err := probeCtx.Err(); err != nil {
			s.logMediaProbe(ctx, artifactID, mediaProbeErrorClass(err))
			return
		}
		media, err := s.prober.Probe(probeCtx, file.Path)
		if err != nil {
			s.logMediaProbe(ctx, artifactID, mediaProbeErrorClass(err))
			if errors.Is(err, domain.ErrMediaProbeUnavailable) {
				// The tool is missing for every file; probing the rest only
				// repeats the same failure.
				return
			}
			continue
		}
		if media.IsZero() {
			// The tool ran but the file yielded nothing usable (for example a
			// damaged or unsupported container).
			s.logMediaProbe(ctx, artifactID, "no_value")
			continue
		}
		targets[index].media = media
	}
}

// logMediaProbe records one bounded, redacted diagnostic. It carries the
// artifact id and an error class only; the raw error and file paths stay out.
func (s *Service) logMediaProbe(ctx context.Context, artifactID, class string) {
	logger := s.logger
	if logger == nil {
		return
	}
	logger.Warn(ctx, observabilityport.RuntimeEvent{
		Message:    "media facts unavailable; resolution, codecs, container and HDR stay empty",
		Component:  mediaProbeComponent,
		Operation:  mediaProbeOperation,
		Outcome:    "degraded",
		ReasonCode: "media_probe_" + class,
		OwnerType:  "artifact",
		OwnerID:    artifactID,
		ErrorClass: class,
	})
}

func mediaProbeErrorClass(err error) string {
	switch {
	case errors.Is(err, domain.ErrMediaProbeUnavailable):
		return "unavailable"
	case errors.Is(err, domain.ErrMediaProbeTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, domain.ErrMediaProbeOutputInvalid):
		return "output_invalid"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "failed"
	}
}
