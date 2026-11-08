import { Injectable, computed, inject, signal } from '@angular/core';
import { Router } from '@angular/router';
import { Observable, Subscription } from 'rxjs';
import { Backend } from './api/backend';
import {
  ApiError,
  Invoice,
  PendingAction,
  Period,
  ServerEvent,
  Summary,
  UnauthorizedError,
} from './api/models';

export type CardState = 'pending' | 'working' | 'done' | 'rejected' | 'failed' | 'expired';

export interface Card {
  action: PendingAction;
  state: CardState;
  invoice?: Invoice;
  error?: string;
}

export interface ChatMessage {
  id: number;
  role: 'user' | 'assistant' | 'error' | 'card';
  text?: string;
  imageName?: string;
  cardId?: string;
}

const BUSY = 'The assistant is busy. Try again in one minute.';

// AppState holds the data both panels share and talks to the backend.
@Injectable({ providedIn: 'root' })
export class AppState {
  private api = inject(Backend);
  private router = inject(Router);
  private nextId = 1;
  private stream?: Subscription;

  readonly messages = signal<ChatMessage[]>([]);
  readonly sending = signal(false);
  readonly cards = signal<Record<string, Card>>({});
  readonly invoices = signal<Invoice[]>([]);
  readonly summary = signal<Summary[]>([]);
  readonly period = signal<Period>('this_month');
  readonly activity = signal<{ at: Date; text: string }[]>([]);
  readonly loadError = signal('');

  readonly pendingCards = computed(() =>
    Object.values(this.cards())
      .filter((c) => c.state === 'pending' || c.state === 'working')
      .sort((a, b) => a.action.expires_at.localeCompare(b.action.expires_at)),
  );

  // start loads the dashboard and opens the live event stream.
  async start(): Promise<void> {
    await this.reload();
    try {
      for (const a of await this.api.actions('PENDING')) {
        this.upsertCard(a);
      }
    } catch (err) {
      this.handle(err);
    }
    this.stream?.unsubscribe();
    this.stream = this.api.events().subscribe((e) => this.onLiveEvent(e));
  }

  stop(): void {
    this.stream?.unsubscribe();
  }

  async reload(): Promise<void> {
    try {
      const [invoices, summary] = await Promise.all([
        this.api.invoices(),
        this.api.summary(this.period()),
      ]);
      this.invoices.set(invoices);
      this.summary.set(summary);
      this.loadError.set('');
    } catch (err) {
      this.handle(err);
    }
  }

  async setPeriod(p: Period): Promise<void> {
    this.period.set(p);
    try {
      this.summary.set(await this.api.summary(p));
    } catch (err) {
      this.handle(err);
    }
  }

  send(text: string, image?: File): void {
    if (this.sending()) {
      return;
    }
    this.push({ role: 'user', text, imageName: image?.name });
    this.sending.set(true);
    this.consume(this.api.chat(text, image), (e) => this.onChatEvent(e), () =>
      this.sending.set(false),
    );
  }

  approve(id: string): void {
    const card = this.cards()[id];
    if (!card || card.state !== 'pending') {
      return;
    }
    this.patchCard(id, { state: 'working', error: undefined });
    let finished = false;
    this.consume(
      this.api.approve(id),
      (e) => {
        if (e.type === 'action.result' && e.data.action_id === id) {
          finished = true;
          this.patchCard(id, { state: 'done', invoice: e.data.invoice });
          if (e.data.invoice) {
            this.upsertInvoice(e.data.invoice);
          }
        } else if (e.type === 'error') {
          finished = true;
          const state: CardState = e.data.code === 'expired' ? 'expired' : 'failed';
          this.patchCard(id, { state, error: e.data.code === 'busy' ? BUSY : e.data.message });
        }
      },
      () => {
        if (!finished) {
          this.patchCard(id, { state: 'failed', error: 'No answer from the server. Try again.' });
        }
      },
    );
  }

  async reject(id: string): Promise<void> {
    try {
      await this.api.reject(id);
      this.patchCard(id, { state: 'rejected' });
    } catch (err) {
      if (err instanceof ApiError) {
        this.patchCard(id, { state: 'failed', error: err.message });
      } else {
        this.handle(err);
      }
    }
  }

  async refresh(invoiceId: string): Promise<void> {
    try {
      this.upsertInvoice(await this.api.refresh(invoiceId));
    } catch (err) {
      this.handle(err);
    }
  }

  // consume subscribes to a stream and sends login errors to /login.
  private consume(
    obs: Observable<ServerEvent>,
    onEvent: (e: ServerEvent) => void,
    onEnd: () => void,
  ): void {
    obs.subscribe({
      next: onEvent,
      error: (err) => {
        this.handle(err);
        onEnd();
      },
      complete: onEnd,
    });
  }

  private onChatEvent(e: ServerEvent): void {
    switch (e.type) {
      case 'message.delta':
        this.push({ role: 'assistant', text: e.data.text });
        break;
      case 'action.proposed':
        this.upsertCard(e.data);
        this.push({ role: 'card', cardId: e.data.id });
        break;
      case 'error':
        this.push({
          role: 'error',
          text: e.data.code === 'busy' || e.data.code === 'rate_limited'
            ? e.data.message || BUSY
            : e.data.message,
        });
        break;
    }
  }

  private onLiveEvent(e: ServerEvent): void {
    switch (e.type) {
      case 'action.proposed':
        if (!this.cards()[e.data.id]) {
          this.upsertCard(e.data);
          this.log(`New card: ${describe(e.data)}`);
        }
        break;
      case 'action.result': {
        const card = this.cards()[e.data.action_id];
        if (card && card.state !== 'done') {
          this.patchCard(e.data.action_id, { state: 'done', invoice: e.data.invoice });
        }
        break;
      }
      case 'invoice.updated':
        this.upsertInvoice(e.data);
        break;
    }
  }

  private upsertInvoice(inv: Invoice): void {
    const before = this.invoices().find((i) => i.id === inv.id);
    this.invoices.update((list) => {
      const rest = list.filter((i) => i.id !== inv.id);
      return [inv, ...rest].sort((a, b) => b.number.localeCompare(a.number));
    });
    if (!before) {
      this.log(`Invoice ${inv.number} for ${inv.client_name} is ${inv.status}.`);
    } else if (before.status !== inv.status) {
      this.log(`Invoice ${inv.number} changed from ${before.status} to ${inv.status}.`);
    }
    this.api.summary(this.period()).then(
      (s) => this.summary.set(s),
      (err) => this.handle(err),
    );
  }

  private upsertCard(a: PendingAction): void {
    this.cards.update((all) => ({
      ...all,
      [a.id]: all[a.id] ?? { action: a, state: a.status === 'PENDING' ? 'pending' : 'done' },
    }));
  }

  private patchCard(id: string, patch: Partial<Card>): void {
    this.cards.update((all) => (all[id] ? { ...all, [id]: { ...all[id], ...patch } } : all));
  }

  private push(m: Omit<ChatMessage, 'id'>): void {
    this.messages.update((list) => [...list, { ...m, id: this.nextId++ }]);
  }

  private log(text: string): void {
    this.activity.update((list) => [{ at: new Date(), text }, ...list].slice(0, 20));
  }

  private handle(err: unknown): void {
    if (err instanceof UnauthorizedError) {
      this.stop();
      this.router.navigateByUrl('/login');
      return;
    }
    const msg = err instanceof Error ? err.message : 'Something went wrong.';
    this.loadError.set(msg);
  }
}

export function describe(a: PendingAction): string {
  const p = a.preview;
  switch (p.kind) {
    case 'invoice':
      return `invoice to ${p.draft.client_name} for ${p.total} ${p.draft.currency}`;
    case 'reminder':
      return `reminder for invoice ${p.invoice.number}`;
    case 'cancel':
      return `cancel invoice ${p.invoice.number}`;
  }
}
