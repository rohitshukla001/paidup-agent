// Package jobs runs background work: the overdue reminder scan.
//
// Task 7 owns this package. The job never sends anything to a client. It
// only creates propose_reminder cards that the user approves or rejects.
// If Gemini is busy, the card uses a plain template note.
package jobs
