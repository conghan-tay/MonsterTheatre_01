package activities

// LogFields name each activity input's room, performance, and turn for the
// worker's activity log lines (telemetry.LogFielder).

func (in ChoicesInput) LogFields() []any { return []any{"room_id", in.RoomID, "turn", in.Turn} }

func (in TagsInput) LogFields() []any { return []any{"room_id", in.RoomID, "turn", in.Turn} }

func (in SpeechInput) LogFields() []any {
	return []any{"room_id", in.RoomID, "performance_id", in.PerformanceID, "turn", in.Turn}
}

func (in LipSyncInputs) LogFields() []any {
	return []any{"room_id", in.RoomID, "performance_id", in.PerformanceID, "turn", in.Turn}
}

func (in LipSyncSubmission) LogFields() []any { return []any{"room_id", in.RoomID, "turn", in.Turn} }

func (in LipSyncWait) LogFields() []any {
	return []any{"room_id", in.RoomID, "performance_id", in.PerformanceID, "turn", in.Turn}
}

func (in CompleteInput) LogFields() []any { return []any{"performance_id", in.PerformanceID} }

func (in FailInput) LogFields() []any {
	return []any{"performance_id", in.PerformanceID, "reason", in.Reason}
}
