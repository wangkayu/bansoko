package reltitletags

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	reltitlemodel "github.com/wangkayu/reltitle/pkg/model"

	"bansoko/internal/rtruntime"
)

// BuiltinVersion is the compiled-in reltitle version without its namespace,
// for the adoption chain's older-than-builtin note.
func BuiltinVersion() string {
	return strings.TrimPrefix(strings.TrimPrefix(ImplementationVersion, "reltitle@"), "v")
}

// externalParseTimeout bounds one out-of-process parse. The binary answers in
// microseconds; a title still unanswered after this is a stuck process, and
// the client's own deadline kills it so the next call restarts it.
const externalParseTimeout = 10 * time.Second

// external is the adopted binary. It is nil while the builtin module parses,
// which is also the state after an adopted binary failed mid-flight: parsing a
// title is more important than keeping the newer dictionary for one request.
var external atomic.Pointer[externalBackend]

// externalBackend is one adopted binary plus the dictionary it reported, so
// projections and descriptors describe the parser that actually ran.
type externalBackend struct {
	client       *rtruntime.Client
	version      string
	line         string
	configDigest string
	vocabulary   []DimensionVocabulary
	known        map[string]map[string]string
}

// externalFailure is the last reason an adopted binary was dropped, kept for
// the status read model. It is not an error path: the builtin parser already
// took over.
var (
	externalFailureMu sync.Mutex
	externalFailure   string
)

// AdoptExternal installs an adopted binary: its dictionary becomes the
// projection dictionary and every parse goes through its process. The caller
// keeps ownership of client and must Close it after UseBuiltin or a later
// AdoptExternal replaced it.
func AdoptExternal(client *rtruntime.Client) error {
	if client == nil {
		return errors.New("no client to adopt")
	}
	ctx, cancel := context.WithTimeout(context.Background(), externalParseTimeout)
	defer cancel()
	dimensions, err := client.Vocabulary(ctx)
	if err != nil {
		return fmt.Errorf("read the adopted dictionary: %w", err)
	}
	vocabulary, known, err := vocabularyFromDimensions(dimensions)
	if err != nil {
		return err
	}
	info := client.Info()
	external.Store(&externalBackend{
		client:       client,
		version:      strings.TrimPrefix(info.Version, "v"),
		line:         info.Line,
		configDigest: info.ConfigDigest,
		vocabulary:   vocabulary,
		known:        known,
	})
	externalFailureMu.Lock()
	externalFailure = ""
	externalFailureMu.Unlock()
	return nil
}

// UseBuiltin drops the adopted binary and parses with the compiled-in module
// again. It is also what runs after an adopted binary fails mid-flight.
func UseBuiltin() {
	external.Store(nil)
}

// ExternalFailure reports why the last adopted binary was dropped, or "".
func ExternalFailure() string {
	externalFailureMu.Lock()
	defer externalFailureMu.Unlock()
	return externalFailure
}

func dropExternal(reason string) {
	external.Store(nil)
	externalFailureMu.Lock()
	externalFailure = reason
	externalFailureMu.Unlock()
}

// activeVersion is the implementation identity of whichever parser answers a
// parse right now.
func activeVersion() string {
	if current := external.Load(); current != nil {
		return "reltitle@" + current.version
	}
	return ImplementationVersion
}

// activeDigest is the dictionary digest of whichever parser answers right now.
func activeDigest() string {
	if current := external.Load(); current != nil {
		return current.configDigest
	}
	return load().configDigest
}

// ActiveVersion is the implementation identity recorded on new evidence.
func ActiveVersion() string { return activeVersion() }

// ActiveDigest is the dictionary digest recorded on new evidence.
func ActiveDigest() string { return activeDigest() }

// parseExternal runs one title through the adopted binary. A transport failure
// drops the adoption so later calls use the builtin parser, and the error is
// returned for this call.
func parseExternal(current *externalBackend, title string) (*reltitlemodel.ParseResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), externalParseTimeout)
	defer cancel()
	result, err := current.client.Parse(ctx, title)
	if err != nil {
		if errors.Is(err, rtruntime.ErrTransport) {
			dropExternal(fmt.Sprintf("adopted reltitle %s stopped answering: %v", current.version, err))
		}
		return nil, err
	}
	return result, nil
}

// vocabularyFromDimensions turns the adopted binary's dictionary into the
// projection vocabulary. Dimensions this package does not publish are ignored;
// a dimension it does publish that the dictionary lacks is a refusal, exactly
// as the builtin path refuses a missing dictionary entry.
func vocabularyFromDimensions(dimensions []rtruntime.Dimension) ([]DimensionVocabulary, map[string]map[string]string, error) {
	byName := make(map[string][]string, len(dimensions))
	for _, dimension := range dimensions {
		byName[dimension.Dimension] = dimension.Values
	}
	vocabulary := make([]DimensionVocabulary, 0, len(Dimensions))
	known := make(map[string]map[string]string, len(Dimensions))
	for _, dimension := range Dimensions {
		rawValues := byName[dimension]
		if wanted, ok := lexiconDimensions[dimension]; ok {
			entry := DimensionVocabulary{Dimension: dimension, Values: make([]string, 0, len(wanted))}
			known[dimension] = make(map[string]string, len(wanted))
			emitted := make(map[string]bool, len(rawValues))
			for _, value := range rawValues {
				emitted[strings.ToLower(value)] = true
			}
			for _, value := range wanted {
				if !emitted[strings.ToLower(value)] {
					return nil, nil, fmt.Errorf("adopted reltitle dictionary has no %s value %s", dimension, value)
				}
				known[dimension][strings.ToLower(value)] = value
				entry.Values = append(entry.Values, value)
			}
			vocabulary = append(vocabulary, entry)
			continue
		}
		if len(rawValues) == 0 {
			return nil, nil, fmt.Errorf("adopted reltitle dictionary has no %s values", dimension)
		}
		entry := DimensionVocabulary{Dimension: dimension, Values: make([]string, 0, len(rawValues))}
		known[dimension] = make(map[string]string, len(rawValues))
		for _, raw := range rawValues {
			value := strings.TrimSpace(raw)
			if value == "" {
				continue
			}
			folded := strings.ToLower(value)
			if _, exists := known[dimension][folded]; exists {
				continue
			}
			known[dimension][folded] = value
			entry.Values = append(entry.Values, value)
		}
		vocabulary = append(vocabulary, entry)
	}
	return vocabulary, known, nil
}
