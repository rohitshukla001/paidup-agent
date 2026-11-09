import { Component, inject, signal } from '@angular/core';
import { DatePipe } from '@angular/common';
import { Invoice } from '../api/models';
import { ApprovalCard } from '../card/approval-card';
import { PeriodMenu } from './period-menu';
import { MoneyPipe, isZero } from '../money';
import { AppState } from '../state';

// bucket matches domain.Bucket on the server.
function bucket(inv: Invoice): string {
  if (inv.status === 'PAID' || inv.status === 'MARKED_AS_PAID') return 'paid';
  if (['SENT', 'UNPAID', 'PARTIALLY_PAID', 'PAYMENT_PENDING'].includes(inv.status)) {
    const today = new Date().toISOString().slice(0, 10);
    return inv.due_date && inv.due_date.slice(0, 10) < today ? 'overdue' : 'unpaid';
  }
  return 'other';
}

@Component({
  selector: 'app-dashboard',
  imports: [ApprovalCard, DatePipe, MoneyPipe, PeriodMenu],
  template: `
    <section class="panel dashboard" aria-labelledby="dash-title">
      <div class="dash-head">
        <h2 id="dash-title">Dashboard</h2>
        <app-period-menu [value]="state.period()" (changed)="state.setPeriod($event)" />
      </div>

      @if (state.loadError()) {
        <p class="error" role="alert">{{ state.loadError() }}</p>
      }

      <div class="totals">
        @for (s of state.summary(); track s.currency) {
          <div class="total paid" [class.zero]="isZero(s.paid)">
            <span>Paid</span><strong>{{ s.paid | money: s.currency }}</strong>
          </div>
          <div class="total unpaid" [class.zero]="isZero(s.unpaid)">
            <span>Unpaid</span><strong>{{ s.unpaid | money: s.currency }}</strong>
          </div>
          <div class="total overdue" [class.zero]="isZero(s.overdue)">
            <span>Overdue</span><strong>{{ s.overdue | money: s.currency }}</strong>
          </div>
        } @empty {
          <p class="muted">No invoices yet. Ask the assistant to bill a client.</p>
        }
      </div>

      @if (state.pendingCards().length) {
        <h3>Waiting for your approval</h3>
        @for (c of state.pendingCards(); track c.action.id) {
          <app-approval-card [card]="c" />
        }
      }

      <h3>Invoices</h3>
      @if (state.invoices().length) {
        <div class="table-wrap">
          <table class="invoices">
            <thead>
              <tr>
                <th scope="col">No.</th>
                <th scope="col">Client</th>
                <th scope="col">Total</th>
                <th scope="col">Due</th>
                <th scope="col">Status</th>
                <th scope="col"><span class="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              @for (inv of state.invoices(); track inv.id) {
                <tr>
                  <td>{{ inv.number }}</td>
                  <td>{{ inv.client_name }}</td>
                  <td>{{ inv.total | money: inv.currency }}</td>
                  <td>{{ inv.due_date ? (inv.due_date | date: 'd MMM' : 'UTC') : '' }}</td>
                  <td><span class="status" [class]="'status ' + bucketOf(inv)">{{ inv.status }}</span></td>
                  <td>
                    <button type="button" class="small" (click)="refresh(inv.id)"
                            [disabled]="refreshing() === inv.id"
                            [attr.aria-label]="'Refresh status of invoice ' + inv.number">
                      {{ refreshing() === inv.id ? '...' : 'Refresh' }}
                    </button>
                  </td>
                </tr>
              }
            </tbody>
          </table>
        </div>
      } @else {
        <p class="muted">Invoices you send appear here.</p>
      }

      <h3>Activity</h3>
      <ul class="activity" aria-live="polite">
        @for (a of state.activity(); track $index) {
          <li><time>{{ a.at | date: 'HH:mm' }}</time> {{ a.text }}</li>
        } @empty {
          <li class="muted">Live updates from PayPal appear here.</li>
        }
      </ul>
    </section>
  `,
})
export class Dashboard {
  protected readonly state = inject(AppState);
  protected readonly refreshing = signal('');
  protected readonly bucketOf = bucket;
  protected readonly isZero = isZero;

  protected async refresh(id: string): Promise<void> {
    this.refreshing.set(id);
    await this.state.refresh(id);
    this.refreshing.set('');
  }
}
