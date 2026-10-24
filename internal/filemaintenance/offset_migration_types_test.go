package filemaintenance

import "testing"

func TestOffsetMigrationGenerationStateIsTerminal(t *testing.T) {
	tests := []struct {
		state OffsetMigrationGenerationState
		want  bool
	}{
		{OffsetMigrationGenerationPending, false},
		{OffsetMigrationGenerationPlanning, false},
		{OffsetMigrationGenerationReady, false},
		{OffsetMigrationGenerationRunning, false},
		{OffsetMigrationGenerationCompleted, true},
		{OffsetMigrationGenerationCompletedWithBlocks, true},
		{OffsetMigrationGenerationFailed, true},
		{OffsetMigrationGenerationSuperseded, true},
		{OffsetMigrationGenerationState("unknown"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := tt.state.IsTerminal(); got != tt.want {
				t.Fatalf("IsTerminal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOffsetMigrationOperationStateSets(t *testing.T) {
	tests := []struct {
		state      OffsetMigrationOperationState
		terminal   bool
		executable bool
	}{
		{OffsetMigrationOperationPlanned, false, false},
		{OffsetMigrationOperationReady, false, true},
		{OffsetMigrationOperationRunning, false, false},
		{OffsetMigrationOperationWaitingRetry, false, false},
		{OffsetMigrationOperationEffectUnknown, false, false},
		{OffsetMigrationOperationApplied, true, false},
		{OffsetMigrationOperationBlocked, true, false},
		{OffsetMigrationOperationFailed, true, false},
		{OffsetMigrationOperationSuperseded, true, false},
		{OffsetMigrationOperationState("unknown"), false, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := tt.state.IsTerminal(); got != tt.terminal {
				t.Errorf("IsTerminal() = %v, want %v", got, tt.terminal)
			}
			if got := tt.state.IsExecutable(); got != tt.executable {
				t.Errorf("IsExecutable() = %v, want %v", got, tt.executable)
			}
		})
	}
}
