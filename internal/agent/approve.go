package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

// getOwnAction returns the action if this session may act on it. An
// action with an empty SessionID came from the reminder job, and any
// logged-in session may act on it.
func (s *Service) getOwnAction(ctx context.Context, sessionID,
	actionID string) (domain.PendingAction, error) {
	a, err := s.store.GetAction(ctx, actionID)
	if err != nil {
		return domain.PendingAction{}, err
	}
	if a.SessionID != "" && a.SessionID != sessionID {
		return domain.PendingAction{}, fmt.Errorf("%w: action %s",
			contract.ErrNotFound, actionID)
	}
	return a, nil
}

// Approve runs the PayPal calls of a pending action, one time.
func (s *Service) Approve(ctx context.Context, sessionID, actionID string,
	emit func(domain.Event)) error {
	a, err := s.getOwnAction(ctx, sessionID, actionID)
	if err != nil {
		if errors.Is(err, contract.ErrNotFound) {
			s.emitError(emit, domain.CodeNotFound, "This card no longer exists.")
		} else {
			s.fail(ctx, emit, "get action", err)
		}
		return ctx.Err()
	}
	if a.Status != domain.ActionPending {
		s.emitError(emit, domain.CodeValidation, alreadyHandled(a.Status))
		return ctx.Err()
	}
	if a.Expired(s.opt.Now()) {
		_, _ = s.store.TransitionAction(ctx, a.ID, domain.ActionPending,
			domain.ActionExpired)
		s.emitError(emit, domain.CodeExpired,
			"This card expired. Ask the assistant again.")
		return ctx.Err()
	}
	won, err := s.store.TransitionAction(ctx, a.ID, domain.ActionPending,
		domain.ActionApproved)
	if err != nil {
		s.fail(ctx, emit, "approve action", err)
		return ctx.Err()
	}
	if !won {
		s.emitError(emit, domain.CodeValidation,
			"This card was already handled.")
		return ctx.Err()
	}

	inv, note, err := s.execute(ctx, a)
	if err != nil {
		s.opt.Logger.Error("agent: execute action", "tool", a.Tool, "err", err)
		res, _ := json.Marshal(map[string]string{"error": err.Error()})
		_ = s.store.SetActionResult(ctx, a.ID, domain.ActionFailed, res)
		s.addNote(ctx, sessionID, "the user approved "+a.Tool+
			" but PayPal returned an error: "+userError(err))
		s.emitError(emit, domain.CodePayPalError,
			"PayPal could not complete this. "+userError(err))
		return ctx.Err()
	}

	res, _ := json.Marshal(map[string]any{"invoice": inv})
	if err := s.store.SetActionResult(ctx, a.ID, domain.ActionDone, res); err != nil {
		s.opt.Logger.Warn("agent: save result", "err", err)
	}
	s.cache(ctx, inv)
	s.addNote(ctx, sessionID, note)
	emitData(emit, domain.EventActionResult, map[string]any{
		"action_id": a.ID, "status": domain.ActionDone, "invoice": inv,
	})
	return nil
}

// execute makes the PayPal calls for an approved action and returns the
// updated invoice and a note for the chat history.
func (s *Service) execute(ctx context.Context,
	a domain.PendingAction) (domain.Invoice, string, error) {
	switch a.Tool {
	case domain.ToolProposeInvoice:
		var d domain.InvoiceDraft
		if err := json.Unmarshal(a.Args, &d); err != nil {
			return domain.Invoice{}, "", err
		}
		inv, err := s.pp.CreateDraft(ctx, d, a.IdempotencyKey("create"))
		if err != nil {
			return domain.Invoice{}, "", err
		}
		if err := s.pp.Send(ctx, inv.ID, a.IdempotencyKey("send")); err != nil {
			return domain.Invoice{}, "", err
		}
		sent, err := s.pp.Get(ctx, inv.ID)
		if err != nil {
			return domain.Invoice{}, "", err
		}
		return sent, fmt.Sprintf("the user approved the invoice. PayPal "+
			"sent invoice %s (ID %s) for %s %s to %s.", sent.Number, sent.ID,
			sent.Total, sent.Currency, sent.ClientEmail), nil

	case domain.ToolProposeReminder:
		var r struct {
			Subject string `json:"subject"`
			Note    string `json:"note"`
		}
		if err := json.Unmarshal(a.Args, &r); err != nil {
			return domain.Invoice{}, "", err
		}
		if err := s.pp.Remind(ctx, a.InvoiceID, r.Subject, r.Note,
			a.IdempotencyKey("remind")); err != nil {
			return domain.Invoice{}, "", err
		}
		if err := s.store.AddReminder(ctx, a.InvoiceID, r.Note); err != nil {
			s.opt.Logger.Warn("agent: record reminder", "err", err)
		}
		inv, err := s.pp.Get(ctx, a.InvoiceID)
		if err != nil {
			return domain.Invoice{}, "", err
		}
		return inv, fmt.Sprintf("the user approved the reminder. PayPal "+
			"sent a reminder for invoice %s.", inv.Number), nil

	case domain.ToolProposeCancel:
		var r struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(a.Args, &r); err != nil {
			return domain.Invoice{}, "", err
		}
		if err := s.pp.Cancel(ctx, a.InvoiceID, r.Reason,
			a.IdempotencyKey("cancel")); err != nil {
			return domain.Invoice{}, "", err
		}
		inv, err := s.pp.Get(ctx, a.InvoiceID)
		if err != nil {
			return domain.Invoice{}, "", err
		}
		return inv, fmt.Sprintf("the user approved the cancellation. "+
			"Invoice %s is now %s.", inv.Number, inv.Status), nil
	}
	return domain.Invoice{}, "", fmt.Errorf("%w: unknown action %q",
		contract.ErrValidation, a.Tool)
}

// Reject marks a pending action as rejected. Nothing goes to PayPal.
func (s *Service) Reject(ctx context.Context, sessionID, actionID string) error {
	a, err := s.getOwnAction(ctx, sessionID, actionID)
	if err != nil {
		return err
	}
	won, err := s.store.TransitionAction(ctx, a.ID, domain.ActionPending,
		domain.ActionRejected)
	if err != nil {
		return err
	}
	if !won {
		return fmt.Errorf("%w: %s", contract.ErrValidation, alreadyHandled(a.Status))
	}
	s.addNote(ctx, sessionID, "the user rejected the "+a.Tool+" card. "+
		"Nothing was sent.")
	return nil
}

func alreadyHandled(st domain.ActionStatus) string {
	switch st {
	case domain.ActionExpired:
		return "This card expired. Ask the assistant again."
	case domain.ActionRejected:
		return "This card was rejected."
	default:
		return "This card was already handled."
	}
}
