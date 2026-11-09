import { Component, computed, inject, input } from '@angular/core';
import { MoneyPipe, money } from '../money';
import { AppState, Card } from '../state';

// ApprovalCard shows one proposed PayPal action with Approve and Reject.
@Component({
  selector: 'app-approval-card',
  imports: [MoneyPipe],
  template: `
    @let c = card();
    @let p = c.action.preview;
    <article class="card" [class]="'card state-' + c.state" [attr.aria-label]="title()">
      <header class="card-head">
        <strong>{{ title() }}</strong>
        <span class="badge" [class]="'badge ' + c.state">{{ label() }}</span>
      </header>

      @switch (p.kind) {
        @case ('invoice') {
          <dl class="facts">
            <dt>Client</dt>
            <dd>{{ p.draft.client_name }} &lt;{{ p.draft.client_email }}&gt;</dd>
            <dt>Due</dt>
            <dd>{{ p.due_date }}</dd>
          </dl>
          <table class="items">
            <thead>
              <tr><th scope="col">Item</th><th scope="col">Qty</th><th scope="col">Price</th></tr>
            </thead>
            <tbody>
              @for (it of p.draft.items; track $index) {
                <tr><td>{{ it.name }}</td><td>{{ it.quantity }}</td><td>{{ it.unit_price | money: p.draft.currency }}</td></tr>
              }
            </tbody>
            <tfoot>
              <tr><th scope="row" colspan="2">Total</th><td>{{ p.total | money: p.draft.currency }}</td></tr>
            </tfoot>
          </table>
          @if (p.draft.note) {
            <p class="note">{{ p.draft.note }}</p>
          }
        }
        @case ('reminder') {
          <dl class="facts">
            <dt>Invoice</dt>
            <dd>{{ p.invoice.number }}, {{ p.invoice.total | money: p.invoice.currency }}</dd>
            <dt>To</dt>
            <dd>{{ p.invoice.client_email }}</dd>
            <dt>Subject</dt>
            <dd>{{ p.subject }}</dd>
          </dl>
          <p class="note">{{ p.note }}</p>
        }
        @case ('cancel') {
          <dl class="facts">
            <dt>Invoice</dt>
            <dd>{{ p.invoice.number }}, {{ p.invoice.total | money: p.invoice.currency }}</dd>
            <dt>Reason</dt>
            <dd>{{ p.reason }}</dd>
          </dl>
        }
      }

      @if (c.error) {
        <p class="error" role="alert">{{ c.error }}</p>
      }
      @if (c.state === 'done' && c.invoice?.payer_view_url) {
        <p class="done-line">
          Invoice {{ c.invoice?.number }} is {{ c.invoice?.status }}.
          <a [href]="c.invoice?.payer_view_url" target="_blank" rel="noopener">Open the payment page</a>
        </p>
      }

      @if (c.state === 'pending' || c.state === 'working') {
        <div class="actions">
          <button class="primary" type="button" (click)="state.approve(c.action.id)"
                  [disabled]="c.state === 'working'">
            {{ c.state === 'working' ? 'Sending to PayPal...' : 'Approve' }}
          </button>
          <button type="button" (click)="state.reject(c.action.id)"
                  [disabled]="c.state === 'working'">Reject</button>
        </div>
      }
    </article>
  `,
})
export class ApprovalCard {
  protected readonly state = inject(AppState);
  readonly card = input.required<Card>();

  protected readonly title = computed(() => {
    const p = this.card().action.preview;
    switch (p.kind) {
      case 'invoice':
        return `Send invoice: ${money(p.total, p.draft.currency)}`;
      case 'reminder':
        return `Send reminder: invoice ${p.invoice.number}`;
      case 'cancel':
        return `Cancel invoice ${p.invoice.number}`;
    }
  });

  protected readonly label = computed(() => {
    switch (this.card().state) {
      case 'pending':
        return 'Waiting for you';
      case 'working':
        return 'Sending';
      case 'done':
        return 'Done';
      case 'rejected':
        return 'Rejected';
      case 'expired':
        return 'Expired';
      default:
        return 'Failed';
    }
  });
}
