package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"

	"bansoko/internal/apperrors"
	"bansoko/internal/filemaintenance/planner"
	"bansoko/internal/filemaintenance/policy"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/parser"
	"bansoko/internal/parserpolicy"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	"bansoko/internal/transport/httpapi/generated"
)

// Service adapts the policy/planner owners to the HTTP contract.
type Service struct {
	policies         *policy.PolicyService
	replacement      *ReplacementPolicyService
	planner          *planner.Planner
	placements       *PlacementService
	decisions        *ReplacementDecisionService
	calibration      *CalibrationService
	cleanup          *CleanupService
	alignment        *AlignmentService
	parserPolicy     ParserPolicyOwner
	fileEffectPolicy FileEffectPolicyOwner
	scanReader       ScanOwnerReader
	pathDiff         *PathDiffService
	animeExists      AnimeExistenceReader
	// recoveryObserver re-stats the candidate's frozen target at confirm
	// time (no-follow, no content read); nil fails closed.
	recoveryObserver RecoveryFileObserver
	// recoveryActivity is the durable business journal of the confirm; nil
	// keeps the confirm silent like every other unwired activity owner.
	recoveryActivity observabilityport.ActivityWriter
	// metadataRefresher is the positive-cache bypass (the 对齐（刷新元数据）
	// variant); nil means the variant fails closed.
	metadataRefresher MetadataWorkRefresher
}

// MetadataWorkRefresher re-pulls the bound works bypassing the positive
// caches. The ownership application adapter implements it; the alignment
// variant calls it through this port so FileMaintenance needs no provider
// wiring of its own.
type MetadataWorkRefresher interface {
	RefreshAnimeMetadataApplication(ctx context.Context, managedAnimeID string) (struct{}, error)
}

// SetMetadataWorkRefresher wires the 对齐（刷新元数据）variant's re-pull port.
func (s *Service) SetMetadataWorkRefresher(refresher MetadataWorkRefresher) {
	if s != nil {
		s.metadataRefresher = refresher
	}
}

// ParserPolicyOwner is the parser policy port (the parserpolicy service
// implements it; FileMaintenance never reads the config tables directly). The
// default is the creation template for future instances; the instance read is
// the frozen per-instance snapshot.
type ParserPolicyOwner interface {
	GetDefault(ctx context.Context) (*parserpolicy.Default, error)
	SaveDefault(ctx context.Context, expectedVersion int64, input parserpolicy.Policy) (parserpolicy.Default, error)
	GetInstance(ctx context.Context, animeID string) (*parserpolicy.Instance, error)
	SaveInstance(ctx context.Context, animeID string, expectedVersion int64, input parserpolicy.Policy) (parserpolicy.Instance, error)
}

// ScanOwnerReader is the read-only scan owner port of the overview surface
// (the reconciliation service owns the scan rows; FileMaintenance never reads
// them directly). A missing scan reports nil without an error.
type ScanOwnerReader interface {
	CurrentScan(ctx context.Context, animeID string) (*reconciliationapplication.ScanView, error)
}

// AnimeExistenceReader verifies managed-anime identity for the overview
// surface. Unknown or deleted instances must fail closed with a typed
// NotFound instead of an empty 200, so the read model can distinguish "no
// owner truth yet" from "no such resource".
type AnimeExistenceReader interface {
	ManagedAnimeExists(ctx context.Context, animeID string) (bool, error)
}

// SQLAnimeExistenceReader adapts the shared pool to the narrow existence read.
// FileMaintenance already reads managed_anime directly in the scan
// scheduler; this stays a single-SELECT identity probe with no ownership of
// the catalog rows.
type SQLAnimeExistenceReader struct{ db *sql.DB }

// NewSQLAnimeExistenceReader wires the shared database pool into the probe.
func NewSQLAnimeExistenceReader(db *sql.DB) *SQLAnimeExistenceReader {
	return &SQLAnimeExistenceReader{db: db}
}

// ManagedAnimeExists reports whether the managed anime row exists. The probe
// never mutates anything and tolerates unbounded identifiers through binding.
func (r *SQLAnimeExistenceReader) ManagedAnimeExists(ctx context.Context, animeID string) (bool, error) {
	if r == nil || r.db == nil {
		return true, nil
	}
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM managed_anime WHERE id = $1)`, animeID).Scan(&exists)
	return exists, err
}

// NewService wires the read/write adapter.
func NewService(policies *policy.PolicyService, replacement *ReplacementPolicyService, planner *planner.Planner, placements *PlacementService, decisions *ReplacementDecisionService) *Service {
	return &Service{policies: policies, replacement: replacement, planner: planner, placements: placements, decisions: decisions}
}

// SetOwnerTruthWires wires the calibration/cleanup/alignment owners and the
// scan reader used by the per-instance overview surface.
func (s *Service) SetOwnerTruthWires(calibration *CalibrationService, cleanup *CleanupService, alignment *AlignmentService, scanReader ScanOwnerReader) {
	if s == nil {
		return
	}
	s.calibration = calibration
	s.cleanup = cleanup
	s.alignment = alignment
	s.scanReader = scanReader
}

// SetParserPolicy wires the per-instance parser policy owner.
func (s *Service) SetParserPolicy(owner ParserPolicyOwner) {
	if s != nil {
		s.parserPolicy = owner
	}
}

// FileEffectPolicyOwner is the frozen per-instance unclassified_route policy
// port (snapshot semantics, never inherited at runtime).
type FileEffectPolicyOwner interface {
	GetDefault(ctx context.Context) (*FileEffectPolicyDefaultView, error)
	SaveDefault(ctx context.Context, expectedVersion int64, value UnclassifiedRoutePolicyValue) (FileEffectPolicyDefaultView, error)
	GetInstance(ctx context.Context, animeID string) (*InstanceFileEffectPolicyView, error)
	SaveInstance(ctx context.Context, animeID string, expectedVersion int64, value UnclassifiedRoutePolicyValue) (InstanceFileEffectPolicyView, error)
}

// SetFileEffectPolicy wires the instance file-effect policy owner.
func (s *Service) SetFileEffectPolicy(owner FileEffectPolicyOwner) {
	if s != nil {
		s.fileEffectPolicy = owner
	}
}

// SetAnimeExistence wires the managed-anime identity probe used by the
// overview surface. Unwired overviews keep returning their aggregated view
// without the identity gate.
func (s *Service) SetAnimeExistence(reader AnimeExistenceReader) {
	if s != nil {
		s.animeExists = reader
	}
}

// GetFileEffectPolicyDefault returns the singleton creation default for
// future instances (absent -> code default with version 0).
func (s *Service) GetFileEffectPolicyDefault(ctx context.Context) (FileEffectPolicyDefaultView, error) {
	if s.fileEffectPolicy == nil {
		return FileEffectPolicyDefaultView{}, &apperrors.NotFound{Resource: "file_effect_policy", ID: "not_wired"}
	}
	view, err := s.fileEffectPolicy.GetDefault(ctx)
	if err != nil {
		return FileEffectPolicyDefaultView{}, err
	}
	if view == nil {
		return FileEffectPolicyDefaultView{UnclassifiedRoute: UnclassifiedRoutePolicyValueSkip, Version: 0}, nil
	}
	return *view, nil
}

// SaveFileEffectPolicyDefault replaces the singleton creation default with
// CAS. Affects future instances only.
func (s *Service) SaveFileEffectPolicyDefault(ctx context.Context, input FileEffectPolicyDefaultInput) (FileEffectPolicyDefaultView, error) {
	if s.fileEffectPolicy == nil {
		return FileEffectPolicyDefaultView{}, &apperrors.NotFound{Resource: "file_effect_policy", ID: "not_wired"}
	}
	return s.fileEffectPolicy.SaveDefault(ctx, input.ExpectedVersion, input.UnclassifiedRoute)
}

// GetInstanceFileEffectPolicy returns the frozen instance file-effect policy.
func (s *Service) GetInstanceFileEffectPolicy(ctx context.Context, animeID string) (InstanceFileEffectPolicyView, error) {
	if s.fileEffectPolicy == nil {
		return InstanceFileEffectPolicyView{}, &apperrors.NotFound{Resource: "file_effect_policy", ID: "not_wired"}
	}
	view, err := s.fileEffectPolicy.GetInstance(ctx, animeID)
	if err != nil {
		return InstanceFileEffectPolicyView{}, err
	}
	if view == nil {
		return InstanceFileEffectPolicyView{}, &apperrors.NotFound{Resource: "file_effect_policy", ID: animeID}
	}
	return *view, nil
}

// SaveInstanceFileEffectPolicy replaces the frozen instance file-effect
// policy with expected-version CAS.
func (s *Service) SaveInstanceFileEffectPolicy(ctx context.Context, animeID string, input InstanceFileEffectPolicyInput) (InstanceFileEffectPolicyView, error) {
	if s.fileEffectPolicy == nil {
		return InstanceFileEffectPolicyView{}, &apperrors.NotFound{Resource: "file_effect_policy", ID: "not_wired"}
	}
	return s.fileEffectPolicy.SaveInstance(ctx, animeID, input.ExpectedVersion, input.UnclassifiedRoute)
}

// GetDefaultParserPolicy returns the singleton creation template for future
// instances. An absent row reports the versioned code default (reltitle)
// with version 0: "never saved", not the version any instance freezes under.
func (s *Service) GetDefaultParserPolicy(ctx context.Context) (ParserPolicyDefaultView, error) {
	if s.parserPolicy == nil {
		return ParserPolicyDefaultView{}, &apperrors.NotFound{Resource: "parser_policy", ID: "not_wired"}
	}
	row, err := s.parserPolicy.GetDefault(ctx)
	if err != nil {
		return ParserPolicyDefaultView{}, err
	}
	if row == nil {
		return ParserPolicyDefaultView{ParserKind: string(parserpolicy.DefaultPolicy().Kind), Version: 0}, nil
	}
	updatedAt := row.UpdatedAt
	return ParserPolicyDefaultView{ParserKind: string(row.Policy.Kind), Version: row.Version, UpdatedAt: &updatedAt}, nil
}

// SaveDefaultParserPolicy replaces the creation template with expected-version
// CAS (0 = create). Affects future instances only: no existing instance is
// rewritten and no Release is re-evaluated.
func (s *Service) SaveDefaultParserPolicy(ctx context.Context, input ParserPolicyDefaultInput) (ParserPolicyDefaultView, error) {
	if s.parserPolicy == nil {
		return ParserPolicyDefaultView{}, &apperrors.NotFound{Resource: "parser_policy", ID: "not_wired"}
	}
	saved, err := s.parserPolicy.SaveDefault(ctx, input.ExpectedVersion, parserpolicy.Policy{Kind: parser.ParserKind(input.ParserKind)})
	if err != nil {
		return ParserPolicyDefaultView{}, err
	}
	updatedAt := saved.UpdatedAt
	return ParserPolicyDefaultView{ParserKind: string(saved.Policy.Kind), Version: saved.Version, UpdatedAt: &updatedAt}, nil
}

// GetInstanceParserPolicy returns the frozen per-instance parser policy.
func (s *Service) GetInstanceParserPolicy(ctx context.Context, animeID string) (parserpolicy.Instance, error) {
	if s.parserPolicy == nil {
		return parserpolicy.Instance{}, &apperrors.NotFound{Resource: "parser_policy", ID: "not_wired"}
	}
	instance, err := s.parserPolicy.GetInstance(ctx, animeID)
	if err != nil {
		return parserpolicy.Instance{}, err
	}
	if instance == nil {
		return parserpolicy.Instance{}, &apperrors.NotFound{Resource: "parser_policy", ID: animeID}
	}
	return *instance, nil
}

// SaveInstanceParserPolicy fully replaces one instance parser policy with
// expected-version CAS. An offset edit plans the affected-Placement scan
// through the parser policy owner's hook after commit.
func (s *Service) SaveInstanceParserPolicy(ctx context.Context, animeID string, input ParserPolicyInput) (parserpolicy.Instance, error) {
	if s.parserPolicy == nil {
		return parserpolicy.Instance{}, &apperrors.NotFound{Resource: "parser_policy", ID: "not_wired"}
	}
	instance, err := s.parserPolicy.SaveInstance(ctx, animeID, input.ExpectedVersion, input.Policy)
	if err != nil {
		return parserpolicy.Instance{}, err
	}
	return instance, nil
}

// GetPolicyApplication returns the global (animeID="") or override policy.
func (s *Service) GetPolicyApplication(ctx context.Context, animeID string) (PolicyView, error) {
	scope := policy.GlobalScope
	if animeID != "" {
		scope = "anime:" + animeID
	}
	row, err := s.policies.GetPolicy(ctx, scope)
	if err != nil {
		return PolicyView{}, err
	}
	if row == nil {
		return PolicyView{
			ScopeKey: scope,
			Safety:   json.RawMessage(`{"max_operations":100}`),
		}, nil
	}
	return PolicyView{
		ScopeKey:            row.ScopeKey,
		Enabled:             row.Enabled,
		ScanIntervalSeconds: cloneIntPointer(row.ScanIntervalSeconds),
		AlignmentEnabled:    cloneBoolPointer(row.AlignmentEnabled),
		Safety:              append(json.RawMessage(nil), row.Safety...),
		Version:             row.Version,
	}, nil
}

// SavePolicyApplication saves one global or per-anime policy with CAS.
func (s *Service) SavePolicyApplication(ctx context.Context, animeID string, input PolicyInput, actor, channel string) (PolicyView, error) {
	scope := policy.GlobalScope
	if animeID != "" {
		scope = "anime:" + animeID
	}
	saved, err := s.policies.SavePolicy(ctx, scope, input.ExpectedVersion, policy.PolicyRow{
		Enabled:             input.Enabled,
		ScanIntervalSeconds: cloneIntPointer(input.ScanIntervalSeconds),
		AlignmentEnabled:    cloneBoolPointer(input.AlignmentEnabled),
		Safety:              append(json.RawMessage(nil), input.Safety...),
	}, actor, channel)
	if err != nil {
		return PolicyView{}, err
	}
	return PolicyView{
		ScopeKey:            saved.ScopeKey,
		Enabled:             saved.Enabled,
		ScanIntervalSeconds: cloneIntPointer(saved.ScanIntervalSeconds),
		AlignmentEnabled:    cloneBoolPointer(saved.AlignmentEnabled),
		Safety:              append(json.RawMessage(nil), saved.Safety...),
		Version:             saved.Version,
	}, nil
}

// GetPolicyView returns the global (animeID="") or override policy.
func (s *Service) GetPolicyView(ctx context.Context, animeID string) (generated.FileMaintenancePolicyView, error) {
	view, err := s.GetPolicyApplication(ctx, animeID)
	if err != nil {
		return generated.FileMaintenancePolicyView{}, err
	}
	return policyViewOfApplication(view), nil
}

// SavePolicyView saves with CAS and returns the new view.
func (s *Service) SavePolicyView(ctx context.Context, animeID string, input generated.FileMaintenancePolicyInput, actor, channel string) (generated.FileMaintenancePolicyView, error) {
	view, err := s.SavePolicyApplication(ctx, animeID, PolicyInput{
		ExpectedVersion:     int64(input.ExpectedVersion),
		Enabled:             input.Enabled,
		ScanIntervalSeconds: cloneIntPointer(input.ScanIntervalSeconds),
		AlignmentEnabled:    cloneBoolPointer(input.AlignmentEnabled),
		Safety:              jsonRawOr(derefMap(input.Safety), `{"max_operations":100}`),
	}, actor, channel)
	if err != nil {
		return generated.FileMaintenancePolicyView{}, err
	}
	return policyViewOfApplication(view), nil
}

func policyViewOfApplication(view PolicyView) generated.FileMaintenancePolicyView {
	return generated.FileMaintenancePolicyView{
		ScopeKey:            view.ScopeKey,
		Enabled:             view.Enabled,
		ScanIntervalSeconds: cloneIntPointer(view.ScanIntervalSeconds),
		AlignmentEnabled:    cloneBoolPointer(view.AlignmentEnabled),
		Safety:              rawMap(view.Safety),
		Version:             int(view.Version),
	}
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// GetReplacementPolicyDefaultApplication returns the singleton creation default.
func (s *Service) GetReplacementPolicyDefaultApplication(ctx context.Context) (ReplacementPolicyDefaultView, error) {
	row, err := s.replacement.GetDefault(ctx)
	if err != nil {
		return ReplacementPolicyDefaultView{}, err
	}
	if row == nil {
		return ReplacementPolicyDefaultView{Policy: DefaultReplacementPolicy()}, nil
	}
	updatedAt := row.UpdatedAt
	return ReplacementPolicyDefaultView{Policy: row.Policy, UpdatedAt: &updatedAt, Version: row.Version}, nil
}

// SaveReplacementPolicyDefaultApplication saves the singleton creation default.
func (s *Service) SaveReplacementPolicyDefaultApplication(ctx context.Context, input ReplacementPolicyWriteInput, actor, channel string) (ReplacementPolicyDefaultView, error) {
	saved, err := s.replacement.SaveDefault(ctx, input.ExpectedVersion, input.Policy, actor, channel)
	if err != nil {
		return ReplacementPolicyDefaultView{}, err
	}
	updatedAt := saved.UpdatedAt
	return ReplacementPolicyDefaultView{Policy: saved.Policy, UpdatedAt: &updatedAt, Version: saved.Version}, nil
}

// GetInstanceReplacementPolicyApplication returns the complete frozen instance policy.
func (s *Service) GetInstanceReplacementPolicyApplication(ctx context.Context, animeID string) (InstanceReplacementPolicyView, error) {
	row, err := s.replacement.GetInstance(ctx, animeID)
	if err != nil {
		return InstanceReplacementPolicyView{}, err
	}
	if row == nil {
		return InstanceReplacementPolicyView{}, &apperrors.NotFound{Resource: "instance_replacement_policy", ID: animeID}
	}
	updatedAt := row.UpdatedAt
	return InstanceReplacementPolicyView{
		ManagedAnimeID: row.ManagedAnimeID, Policy: row.Policy, SourceDefaultVersion: row.SourceDefaultVersion,
		UpdatedAt: &updatedAt, Version: row.Version,
	}, nil
}

// SaveInstanceReplacementPolicyApplication fully replaces one frozen instance policy.
func (s *Service) SaveInstanceReplacementPolicyApplication(ctx context.Context, animeID string, input ReplacementPolicyWriteInput, actor, channel string) (InstanceReplacementPolicyView, error) {
	saved, err := s.replacement.SaveInstance(ctx, animeID, input.ExpectedVersion, input.Policy, actor, channel)
	if err != nil {
		return InstanceReplacementPolicyView{}, err
	}
	updatedAt := saved.UpdatedAt
	return InstanceReplacementPolicyView{
		ManagedAnimeID: saved.ManagedAnimeID, Policy: saved.Policy, SourceDefaultVersion: saved.SourceDefaultVersion,
		UpdatedAt: &updatedAt, Version: saved.Version,
	}, nil
}

// GetReplacementPolicyDefault returns the singleton creation default in the generated compatibility shape.
func (s *Service) GetReplacementPolicyDefault(ctx context.Context) (generated.ReplacementPolicyDefaultView, error) {
	view, err := s.GetReplacementPolicyDefaultApplication(ctx)
	if err != nil {
		return generated.ReplacementPolicyDefaultView{}, err
	}
	return replacementDefaultViewOfApplication(view), nil
}

// SaveReplacementPolicyDefault saves the singleton creation default with CAS.
func (s *Service) SaveReplacementPolicyDefault(ctx context.Context, input generated.ReplacementPolicyDefaultInput, actor, channel string) (generated.ReplacementPolicyDefaultView, error) {
	policy, err := replacementPolicyFromInput(input.AdmissionMode, input.QualityProfile)
	if err != nil {
		return generated.ReplacementPolicyDefaultView{}, err
	}
	view, err := s.SaveReplacementPolicyDefaultApplication(ctx, ReplacementPolicyWriteInput{ExpectedVersion: int64(input.ExpectedVersion), Policy: policy}, actor, channel)
	if err != nil {
		return generated.ReplacementPolicyDefaultView{}, err
	}
	return replacementDefaultViewOfApplication(view), nil
}

// GetInstanceReplacementPolicy returns the complete frozen instance policy in the generated compatibility shape.
func (s *Service) GetInstanceReplacementPolicy(ctx context.Context, animeID string) (generated.InstanceReplacementPolicyView, error) {
	view, err := s.GetInstanceReplacementPolicyApplication(ctx, animeID)
	if err != nil {
		return generated.InstanceReplacementPolicyView{}, err
	}
	return instanceReplacementPolicyViewOfApplication(view), nil
}

// SaveInstanceReplacementPolicy fully replaces one instance policy with CAS.
func (s *Service) SaveInstanceReplacementPolicy(ctx context.Context, animeID string, input generated.InstanceReplacementPolicyInput, actor, channel string) (generated.InstanceReplacementPolicyView, error) {
	policy, err := replacementPolicyFromInput(input.AdmissionMode, input.QualityProfile)
	if err != nil {
		return generated.InstanceReplacementPolicyView{}, err
	}
	view, err := s.SaveInstanceReplacementPolicyApplication(ctx, animeID, ReplacementPolicyWriteInput{ExpectedVersion: int64(input.ExpectedVersion), Policy: policy}, actor, channel)
	if err != nil {
		return generated.InstanceReplacementPolicyView{}, err
	}
	return instanceReplacementPolicyViewOfApplication(view), nil
}

// wireJSON converts a stored JSON document into the generated wire shape
// (typed known fields plus additional properties; unknown fields survive via
// the generated AdditionalProperties handling).
func wireJSON[T any](raw json.RawMessage) T {
	var view T
	_ = json.Unmarshal(raw, &view)
	return view
}

func replacementDefaultViewOfApplication(view ReplacementPolicyDefaultView) generated.ReplacementPolicyDefaultView {
	return generated.ReplacementPolicyDefaultView{
		AdmissionMode:  generated.ReplacementAdmissionMode(view.Policy.AdmissionMode),
		QualityProfile: wireJSON[generated.QualityProfile](view.Policy.QualityProfile),

		UpdatedAt: cloneTimePointer(view.UpdatedAt),
		Version:   int(view.Version),
	}
}

func instanceReplacementPolicyViewOfApplication(view InstanceReplacementPolicyView) generated.InstanceReplacementPolicyView {
	return generated.InstanceReplacementPolicyView{
		ManagedAnimeId: generated.ID(view.ManagedAnimeID),
		AdmissionMode:  generated.ReplacementAdmissionMode(view.Policy.AdmissionMode),
		QualityProfile: wireJSON[generated.QualityProfile](view.Policy.QualityProfile),

		SourceDefaultVersion: int(view.SourceDefaultVersion),
		UpdatedAt:            cloneTimePointer(view.UpdatedAt),
		Version:              int(view.Version),
	}
}

// owner value. The profile wire shape is a typed object; a JSON round-trip
// normalizes it into a plain map. An omitted value normalizes to the empty
// object (a complete, constructable value), never to JSON null.
func replacementPolicyFromInput(mode generated.ReplacementAdmissionMode, profile any) (ReplacementPolicy, error) {
	profileRaw, err := anyToRaw(profile)
	if err != nil {
		return ReplacementPolicy{}, err
	}
	return ReplacementPolicy{
		AdmissionMode:  AdmissionMode(mode),
		QualityProfile: profileRaw,
	}, nil
}

// anyToRaw marshals a wire value into its JSON document; nil becomes {}.
func anyToRaw(value any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{}`), nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func rawMap(raw json.RawMessage) map[string]interface{} {
	var value map[string]interface{}
	_ = json.Unmarshal(raw, &value)
	if value == nil {
		return map[string]interface{}{}
	}
	return value
}

func mapValue(value map[string]any) map[string]interface{} {
	if value == nil {
		return nil
	}
	result := make(map[string]interface{}, len(value))
	for key, item := range value {
		result[key] = cloneValue(item)
	}
	return result
}

func cloneValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		return mapValue(item)
	case []any:
		result := make([]any, len(item))
		for index, child := range item {
			result[index] = cloneValue(child)
		}
		return result
	default:
		return value
	}
}

// ReplanApplication runs the deterministic dry-run for one completed generation.
func (s *Service) ReplanApplication(ctx context.Context, input ReplanInput) (planner.PlanResult, error) {
	effective, err := s.policies.GetEffective(ctx, input.ManagedAnimeID)
	if err != nil {
		return planner.PlanResult{}, err
	}
	plan, err := s.planner.Plan(ctx, input.ManagedAnimeID, input.Generation, effective.PolicyVersion)
	if err != nil {
		return planner.PlanResult{}, err
	}
	return *plan, nil
}

// Replan runs the deterministic dry-run for one completed generation in the
// generated compatibility shape.
func (s *Service) Replan(ctx context.Context, input generated.FileMaintenanceReplanInput) (generated.FileMaintenanceReplanResult, error) {
	plan, err := s.ReplanApplication(ctx, ReplanInput{
		ManagedAnimeID: string(input.ManagedAnimeId),
		Generation:     int64(input.Generation),
	})
	if err != nil {
		return generated.FileMaintenanceReplanResult{}, err
	}
	return generated.FileMaintenanceReplanResult{
		AnimeId:          plan.AnimeID,
		Generation:       int(plan.Generation),
		PolicyVersion:    int(plan.PolicyVersion),
		InputFingerprint: plan.InputFingerprint,
		Candidates:       candidateViews(plan.Candidates),
	}, nil
}

func candidateViews(candidates []planner.PlanCandidate) []generated.FileMaintenancePlanCandidate {
	views := make([]generated.FileMaintenancePlanCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		view := generated.FileMaintenancePlanCandidate{
			ObservationRef: candidate.ObservationRef,
			SourcePath:     candidate.SourcePath,
			Fingerprint:    candidate.Fingerprint,
			SizeBytes:      int(candidate.SizeBytes),
			Coverage:       candidate.Coverage,
			Excluded:       &candidate.Excluded,
		}
		if candidate.TargetPath != "" {
			view.TargetPath = &candidate.TargetPath
		}
		if candidate.ExcludeReason != "" {
			view.ExcludeReason = &candidate.ExcludeReason
		}
		if candidate.ExcludeReasonCode != "" {
			view.ExcludeReasonCode = &candidate.ExcludeReasonCode
		}
		views = append(views, view)
	}
	return views
}

func derefMap(value *map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	return *value
}

func jsonRawOr(value map[string]any, fallback string) json.RawMessage {
	if value != nil {
		raw, err := json.Marshal(value)
		if err == nil {
			return raw
		}
	}
	return json.RawMessage(fallback)
}

// ListPlacementViews returns verified placements as application-owned values.
// HTTP transport projects these values onto generated contract types.
func (s *Service) ListPlacementViews(ctx context.Context, animeID string, limit, offset int) ([]PlacementView, error) {
	return s.placements.ListPlacementsApplication(ctx, animeID, limit, offset)
}

// GetPlacementCoverageViews returns placement coverage as application-owned
// values. HTTP transport projects these values onto generated contract types.
func (s *Service) GetPlacementCoverageViews(ctx context.Context, placementID string) ([]PlacementCoverageView, error) {
	return s.placements.GetCoverageApplication(ctx, placementID)
}

// ListPlacements adapts the placement owner to the HTTP contract.
func (s *Service) ListPlacements(ctx context.Context, animeID string, limit, offset int) ([]generated.PlacementView, error) {
	return s.placements.ListPlacements(ctx, animeID, limit, offset)
}

// GetPlacementCoverage returns the full coverage set.
func (s *Service) GetPlacementCoverage(ctx context.Context, placementID string) ([]generated.PlacementCoverageView, error) {
	return s.placements.GetCoverage(ctx, placementID)
}

// ListDecisionViews returns persisted replacement decisions as application-owned values.
func (s *Service) ListDecisionViews(ctx context.Context, animeID string, limit, offset int) ([]ReplacementDecisionView, error) {
	if s.decisions == nil {
		return nil, &apperrors.NotFound{Resource: "replacement_decision", ID: "not_wired"}
	}
	return s.decisions.ListDecisionsApplication(ctx, animeID, limit, offset)
}

// ListDecisions adapts the decision owner to the HTTP contract.
func (s *Service) ListDecisions(ctx context.Context, animeID string, limit, offset int) ([]generated.ReplacementDecisionView, error) {
	return s.decisions.ListDecisions(ctx, animeID, limit, offset)
}

// EvaluateDecisionApplication evaluates and persists the idempotent per-key
// replacement decisions.
func (s *Service) EvaluateDecisionApplication(ctx context.Context, input EvaluateRequest) ([]ReplacementDecisionView, error) {
	return s.decisions.EvaluateApplication(ctx, input)
}

// GetOverviewApplication returns the minimal per-instance FileMaintenance truth
// as application-owned values. It is a pure read aggregation over typed owner
// services and never scans, plans, or mutates anything.
func (s *Service) GetOverviewApplication(ctx context.Context, animeID string) (OverviewView, error) {
	if s.animeExists != nil {
		exists, err := s.animeExists.ManagedAnimeExists(ctx, animeID)
		if err != nil {
			return OverviewView{}, err
		}
		if !exists {
			return OverviewView{}, &apperrors.NotFound{Resource: "managed_anime", ID: animeID}
		}
	}
	view := OverviewView{ManagedAnimeID: animeID}
	if s.scanReader != nil {
		scan, err := s.scanReader.CurrentScan(ctx, animeID)
		if err != nil {
			return OverviewView{}, err
		}
		view.Scan = scan
	}
	if s.calibration != nil {
		consumers, err := s.calibration.ListConsumerCheckpointsApplication(ctx, animeID, 200, 0)
		if err != nil {
			return OverviewView{}, err
		}
		view.Consumers = &consumers
	}
	if s.placements != nil {
		placements, err := s.placements.ListPlacementsAllStates(ctx, animeID, 200, 0)
		if err != nil {
			return OverviewView{}, err
		}
		view.Placements = &placements
		issues, err := s.placements.ListPlacementIssues(ctx, animeID)
		if err != nil {
			return OverviewView{}, err
		}
		view.PlacementIssues = &issues
	}
	if s.decisions != nil {
		decisions, err := s.decisions.ListDecisionsApplication(ctx, animeID, 200, 0)
		if err != nil {
			return OverviewView{}, err
		}
		view.Decisions = &decisions
	}
	if s.cleanup != nil {
		deletions, err := s.cleanup.ListReplacementDeletionsApplication(ctx, animeID, 200, 0)
		if err != nil {
			return OverviewView{}, err
		}
		view.Deletions = &deletions
	}
	if s.alignment != nil {
		alignments, err := s.alignment.ListAlignmentsApplication(ctx, animeID, 200, 0)
		if err != nil {
			return OverviewView{}, err
		}
		view.Alignments = &alignments
	}
	if s.pathDiff != nil && s.pathDiff.renderer != nil {
		issues, err := s.pathDiff.CompanionIssues(ctx, animeID)
		if err != nil {
			return OverviewView{}, err
		}
		view.CompanionIssues = &issues
	}
	if s.replacement != nil {
		instance, err := s.GetInstanceReplacementPolicyApplication(ctx, animeID)
		if err != nil {
			if _, ok := err.(*apperrors.NotFound); !ok {
				return OverviewView{}, err
			}
		} else {
			view.InstancePolicy = &instance
		}
		global, err := s.replacement.GetDefault(ctx)
		if err != nil {
			return OverviewView{}, err
		}
		if global != nil {
			updatedAt := global.UpdatedAt
			view.GlobalDefault = &ReplacementPolicyDefaultView{Policy: global.Policy, UpdatedAt: &updatedAt, Version: global.Version}
		}
	}
	return view, nil
}

// GetOverview returns the aggregate in the generated compatibility shape.
func (s *Service) GetOverview(ctx context.Context, animeID string) (generated.FileMaintenanceOverviewView, error) {
	view, err := s.GetOverviewApplication(ctx, animeID)
	if err != nil {
		return generated.FileMaintenanceOverviewView{}, err
	}
	result := generated.FileMaintenanceOverviewView{ManagedAnimeId: generated.ID(view.ManagedAnimeID)}
	if view.Scan != nil {
		result.Scan = &generated.ReconciliationScanView{
			CapacityStats:    mapValue(view.Scan.CapacityStats),
			CompletedAt:      cloneTimePointer(view.Scan.CompletedAt),
			FailureCode:      cloneStringPointer(view.Scan.FailureCode),
			FailureReason:    cloneStringPointer(view.Scan.FailureReason),
			Generation:       int(view.Scan.Generation),
			InputFingerprint: view.Scan.InputFingerprint,
			ManagedAnimeId:   generated.ID(view.Scan.ManagedAnimeID),
			Progress:         mapValue(view.Scan.Progress),
			StartedAt:        cloneTimePointer(view.Scan.StartedAt),
			State:            generated.ReconciliationScanViewState(view.Scan.State),
			Version:          int(view.Scan.Version),
		}
	}
	if view.Consumers != nil {
		consumers := make([]generated.ReconciliationConsumerStateView, len(*view.Consumers))
		for index, consumer := range *view.Consumers {
			consumers[index] = reconciliationConsumerStateViewToGenerated(consumer)
		}
		result.Consumers = &consumers
	}
	if view.Placements != nil {
		placements := placementViewsToGenerated(*view.Placements)
		result.Placements = &placements
	}
	if view.Decisions != nil {
		decisions := replacementDecisionViewsToGenerated(*view.Decisions)
		result.Decisions = &decisions
	}
	if view.Deletions != nil {
		deletions := make([]generated.ReplacementDeletionView, len(*view.Deletions))
		for index, cleanup := range *view.Deletions {
			deletions[index] = replacementDeletionViewToGenerated(cleanup)
		}
		result.Deletions = &deletions
	}
	if view.Alignments != nil {
		alignments := make([]generated.AlignmentOperationView, len(*view.Alignments))
		for index, alignment := range *view.Alignments {
			alignments[index] = alignmentOperationViewToGenerated(alignment)
		}
		result.Alignments = &alignments
	}
	if view.InstancePolicy != nil {
		instance := instanceReplacementPolicyViewOfApplication(*view.InstancePolicy)
		result.InstancePolicy = &instance
	}
	if view.GlobalDefault != nil {
		global := replacementDefaultViewOfApplication(*view.GlobalDefault)
		result.GlobalDefault = &global
	}
	return result, nil
}

// SetPathDiff wires the reconciliation path-difference preview owner.
func (s *Service) SetPathDiff(service *PathDiffService) {
	if s != nil {
		s.pathDiff = service
	}
}

// ListReconciliationPathDiffs returns the calibration rows of one generation
// (0 = latest) with their rendered expected target and the differs flag
// (side-effect free preview).
func (s *Service) ListReconciliationPathDiffs(ctx context.Context, animeID string, generation int64, limit, offset int) ([]PathDiffView, error) {
	if s.pathDiff == nil {
		return nil, &apperrors.NotFound{Resource: "path_diff", ID: "not_wired"}
	}
	return s.pathDiff.ListPathDiffs(ctx, animeID, generation, limit, offset)
}
