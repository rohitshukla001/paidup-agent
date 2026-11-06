// Package agent runs the chat loop, the tools, and the approval flow.
//
// Task 4 owns this package. The model can read invoices freely, but every
// write goes through a PendingAction that the user must approve. Approve
// moves the action from PENDING to APPROVED in one atomic step, so two
// clicks on the same card make one set of PayPal calls.
//
// HandleMessage and Approve report problems to the user as error events
// and return an error only when the context ends.
package agent
