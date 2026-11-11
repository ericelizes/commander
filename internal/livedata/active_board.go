package livedata

// Active orchestration reads the shared next-actor decision.
func (b board) activeWorkstreamLine(ws SnapWorkstream, asks int) line {
	return actorLine(b.actorRow(ws))
}
