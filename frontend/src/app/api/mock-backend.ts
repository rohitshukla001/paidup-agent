import { Injectable } from '@angular/core';
import { Observable, Subject } from 'rxjs';
import { Backend } from './backend';
import {
  ApiError,
  ChatItem,
  Invoice,
  InvoiceDraft,
  PendingAction,
  Period,
  ServerEvent,
  Summary,
} from './models';

// MockBackend runs the demo script with no server. The password is
// "demo". An approved invoice is paid 4 seconds later, so the live
// dashboard update can be seen.

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));
const isoDate = (d: Date) => d.toISOString().slice(0, 10);

function daysFromNow(days: number): Date {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() + days);
  return d;
}

// draftFrom pulls an email, an amount, and a day count out of the text.
function draftFrom(text: string): InvoiceDraft {
  const email = text.match(/[\w.+-]+@[\w-]+\.[\w.]+/)?.[0] ?? 'jane@example.com';
  const amount = text.match(/(\d[\d,]*(?:\.\d{1,2})?)\s*(USD|EUR|GBP|CAD|AUD)?/i);
  const days = text.match(/(\d+)\s*days?/i);
  const name = email.split('@')[0].replace(/[._]/g, ' ');
  return {
    client_name: name.charAt(0).toUpperCase() + name.slice(1),
    client_email: email,
    currency: (amount?.[2] ?? 'USD').toUpperCase(),
    items: [
      {
        name: 'Landing page',
        quantity: 1,
        unit_price: Number((amount?.[1] ?? '450').replace(/,/g, '')).toFixed(2),
      },
    ],
    due_days: days ? Number(days[1]) : 7,
    note: '',
  };
}

@Injectable()
export class MockBackend extends Backend {
  private loggedIn = false;
  private seq = 7;
  private invoices$ = new Map<string, Invoice>();
  private actions$ = new Map<string, PendingAction & { args: unknown }>();
  private live = new Subject<ServerEvent>();

  constructor() {
    super();
    const overdue: Invoice = {
      id: 'INV2-MOCK-0006',
      number: '0006',
      status: 'SENT',
      client_name: 'Tom Baker',
      client_email: 'tom.baker@example.net',
      currency: 'GBP',
      total: '970.00',
      due_date: isoDate(daysFromNow(-5)),
      payer_view_url: 'https://www.sandbox.paypal.com/invoice/p/#INV2-MOCK-0006',
      updated_at: new Date().toISOString(),
    };
    this.invoices$.set(overdue.id, overdue);
    // The reminder job's card for the overdue invoice.
    this.actions$.set('job-1', {
      id: 'job-1',
      session_id: '',
      tool: 'propose_reminder',
      invoice_id: overdue.id,
      status: 'PENDING',
      expires_at: daysFromNow(3).toISOString(),
      preview: {
        kind: 'reminder',
        invoice: overdue,
        subject: 'Reminder: invoice 0006 is overdue',
        note: 'Hi Tom, invoice 0006 for 970.00 GBP is now 5 days overdue. You can pay it with the link in the original email. Thank you!',
      },
      args: {},
    });
  }

  async session(): Promise<boolean> {
    return this.loggedIn;
  }

  async login(password: string): Promise<void> {
    await wait(200);
    if (password !== 'demo') {
      throw new ApiError('unauthorized', 'Wrong password. The mock password is "demo".');
    }
    this.loggedIn = true;
  }

  chat(text: string, image?: File): Observable<ServerEvent> {
    return new Observable<ServerEvent>((sub) => {
      let stopped = false;
      (async () => {
        await wait(600);
        if (stopped) return;
        if (/owe|unpaid|overdue|who/i.test(text) && !image) {
          const open = [...this.invoices$.values()].filter((i) =>
            ['SENT', 'UNPAID'].includes(i.status),
          );
          const reply = open.length
            ? 'These clients owe you money: ' +
              open.map((i) => `${i.client_name} (${i.total} ${i.currency})`).join(', ') +
              '.'
            : 'Nobody owes you money right now.';
          sub.next({ type: 'message.delta', data: { text: reply } });
        } else {
          const draft = image
            ? { ...draftFrom('maria.lopez@example.com 1580 USD due in 10 days'), client_name: 'Maria Lopez' }
            : draftFrom(text);
          const total = (Number(draft.items[0].unit_price) * draft.items[0].quantity).toFixed(2);
          const action = {
            id: `act-${Date.now()}`,
            session_id: 'mock',
            tool: 'propose_invoice',
            invoice_id: '',
            status: 'PENDING' as const,
            expires_at: new Date(Date.now() + 15 * 60_000).toISOString(),
            preview: {
              kind: 'invoice' as const,
              draft,
              total,
              due_date: isoDate(daysFromNow(draft.due_days)),
            },
            args: draft,
          };
          this.actions$.set(action.id, action);
          sub.next({ type: 'action.proposed', data: action });
          sub.next({
            type: 'message.delta',
            data: { text: 'I prepared the invoice. Check the card and approve it to send.' },
          });
        }
        sub.next({ type: 'message.done', data: { message_id: `m-${Date.now()}` } });
        sub.complete();
      })();
      return () => (stopped = true);
    });
  }

  approve(actionId: string): Observable<ServerEvent> {
    return new Observable<ServerEvent>((sub) => {
      (async () => {
        await wait(900);
        const a = this.actions$.get(actionId);
        if (!a || a.status !== 'PENDING') {
          sub.next({
            type: 'error',
            data: { code: 'validation', message: 'This card was already handled.' },
          });
          sub.complete();
          return;
        }
        a.status = 'DONE';
        let invoice: Invoice;
        if (a.preview.kind === 'invoice') {
          const id = `INV2-MOCK-${String(++this.seq).padStart(4, '0')}`;
          invoice = {
            id,
            number: String(this.seq).padStart(4, '0'),
            status: 'SENT',
            client_name: a.preview.draft.client_name,
            client_email: a.preview.draft.client_email,
            currency: a.preview.draft.currency,
            total: a.preview.total,
            due_date: a.preview.due_date,
            payer_view_url: `https://www.sandbox.paypal.com/invoice/p/#${id}`,
            updated_at: new Date().toISOString(),
          };
          this.invoices$.set(id, invoice);
          this.payLater(id);
        } else {
          invoice = this.invoices$.get(a.invoice_id)!;
          if (a.preview.kind === 'cancel') {
            invoice = { ...invoice, status: 'CANCELLED' };
            this.invoices$.set(invoice.id, invoice);
          }
        }
        const result: ServerEvent = {
          type: 'action.result',
          data: { action_id: actionId, status: 'DONE', invoice },
        };
        sub.next(result);
        this.live.next(result);
        this.live.next({ type: 'invoice.updated', data: invoice });
        sub.complete();
      })();
    });
  }

  // payLater marks an invoice as paid, as if the client used the link.
  private payLater(id: string) {
    setTimeout(() => {
      const inv = this.invoices$.get(id);
      if (!inv) return;
      const paid = { ...inv, status: 'PAID', updated_at: new Date().toISOString() };
      this.invoices$.set(id, paid);
      this.live.next({ type: 'invoice.updated', data: paid });
    }, 4000);
  }

  // The mock keeps its data in memory, so a reload starts a new chat.
  async history(): Promise<ChatItem[]> {
    return [];
  }

  async reject(actionId: string): Promise<void> {
    const a = this.actions$.get(actionId);
    if (!a || a.status !== 'PENDING') {
      throw new ApiError('validation', 'This card was already handled.');
    }
    a.status = 'REJECTED';
  }

  async actions(status: 'PENDING' | ''): Promise<PendingAction[]> {
    return [...this.actions$.values()].filter((a) => !status || a.status === status);
  }

  async invoices(): Promise<Invoice[]> {
    return [...this.invoices$.values()].sort((a, b) => b.number.localeCompare(a.number));
  }

  async summary(_period: Period): Promise<Summary[]> {
    const by = new Map<string, Summary>();
    const today = isoDate(new Date());
    for (const inv of this.invoices$.values()) {
      const s = by.get(inv.currency) ?? {
        currency: inv.currency,
        paid: '0.00',
        unpaid: '0.00',
        overdue: '0.00',
      };
      const add = (k: 'paid' | 'unpaid' | 'overdue') =>
        (s[k] = (Number(s[k]) + Number(inv.total)).toFixed(2));
      if (inv.status === 'PAID') add('paid');
      else if (inv.status === 'SENT') add(inv.due_date < today ? 'overdue' : 'unpaid');
      by.set(inv.currency, s);
    }
    return [...by.values()].sort((a, b) => a.currency.localeCompare(b.currency));
  }

  async refresh(invoiceId: string): Promise<Invoice> {
    const inv = this.invoices$.get(invoiceId);
    if (!inv) throw new ApiError('not_found', 'Not found.');
    return inv;
  }

  events(): Observable<ServerEvent> {
    return this.live.asObservable();
  }
}
