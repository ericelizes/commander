package livedata

import (
	"regexp"
	"strings"
)

var ticketShape = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]{1,9}-\d{1,6}`)

// TitleTickets returns what could be Linear ids in a title, upper-cased, as
// the app's ticketIds does: bounded by anything but a letter, a digit or (in
// front) a hyphen, so "op_TEAM-12" counts and "x-TEAM-12" does not. "utf-8" comes
// back as UTF-8: callers match the result against real issue ids.
func TitleTickets(title string) []string {
	var out []string
	for _, m := range ticketShape.FindAllStringIndex(title, -1) {
		if m[0] > 0 && (isTicketChar(title[m[0]-1]) || title[m[0]-1] == '-') {
			continue
		}
		if m[1] < len(title) && isTicketChar(title[m[1]]) {
			continue
		}
		out = append(out, strings.ToUpper(title[m[0]:m[1]]))
	}
	return out
}

func isTicketChar(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// WorkstreamTicket is a ticket a workstream carries, in a title or a ticket:
// label, on itself or on an open track. Working says the track has a live
// Operator; a ticket on the workstream itself is judged by the row's own count.
type WorkstreamTicket struct {
	ID      string
	OnTrack bool
	Working bool
}

// WorkstreamTickets is the join of Linear tickets to a workstream, from the raw
// title (the board's may have the id dropped). Only open tracks claim a ticket,
// and each is judged alone: one track's Operator says nothing about its sibling.
func WorkstreamTickets(ws SnapWorkstream) []WorkstreamTicket {
	var out []WorkstreamTicket
	for _, id := range append(TitleTickets(ws.Title), ws.Tickets...) {
		out = append(out, WorkstreamTicket{ID: id})
	}
	for _, t := range openTracks(ws) {
		if parked(t) {
			continue
		}
		working := workingOperators(SnapWorkstream{Steps: []SnapStep{t}}) > 0
		for _, id := range append(TitleTickets(t.Title), t.Tickets...) {
			out = append(out, WorkstreamTicket{ID: id, OnTrack: true, Working: working})
		}
	}
	return out
}
