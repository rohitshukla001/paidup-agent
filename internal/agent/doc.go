// Package agent runs the chat loop, the tools, and the approval flow.
//
// Task 4 owns this package. Export:
//
//	agent.New(l contract.LLM, p contract.PayPal,
//		s contract.Store) contract.Agent
//
// See Task 4 in docs/implementation-plan.md.
package agent
