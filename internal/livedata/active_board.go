package livedata

// Active orchestration: a row reads its next-actor state (see nextactor), the
// same decision supervision acts on. A chat turn is not execution.
func (b board) activeWorkstreamLine(ws SnapWorkstream, asks int) line {
	return actorLine(b.actorRow(ws))
}
