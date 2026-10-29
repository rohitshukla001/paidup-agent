// Package jobs runs background work, such as the overdue reminder scan.
//
// Task 7 owns this package. Export:
//
//	jobs.NewReminderJob(p contract.PayPal, s contract.Store,
//		l contract.LLM, b contract.Bus) *jobs.ReminderJob
//
// ReminderJob has the method Run(ctx context.Context).
// See Task 7 in docs/implementation-plan.md.
package jobs
