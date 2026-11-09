import { Injectable } from '@angular/core';
import { Observable } from 'rxjs';
import { Backend } from './backend';
import {
  ApiError,
  ChatItem,
  ErrorData,
  Invoice,
  PendingAction,
  Period,
  ServerEvent,
  Summary,
  UnauthorizedError,
} from './models';
import { readSSE } from './sse';

const EVENT_TYPES = ['action.proposed', 'action.result', 'invoice.updated'];

@Injectable()
export class HttpBackend extends Backend {
  private async request(path: string, init: RequestInit = {}): Promise<Response> {
    const res = await fetch(path, { credentials: 'same-origin', ...init });
    if (res.status === 401 && path !== '/api/login') {
      throw new UnauthorizedError();
    }
    if (!res.ok) {
      let err: ErrorData = { code: 'internal', message: `Request failed (${res.status}).` };
      try {
        err = await res.json();
      } catch {
        // Not JSON: keep the generic message.
      }
      throw new ApiError(err.code, err.message);
    }
    return res;
  }

  private async json<T>(path: string, init?: RequestInit): Promise<T> {
    return (await this.request(path, init)).json() as Promise<T>;
  }

  async session(): Promise<boolean> {
    const res = await fetch('/api/session', { credentials: 'same-origin' });
    return res.status === 204;
  }

  async login(password: string): Promise<void> {
    await this.request('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    });
  }

  // stream runs a POST that answers with SSE.
  private stream(path: string, body?: FormData): Observable<ServerEvent> {
    return new Observable<ServerEvent>((sub) => {
      const ctrl = new AbortController();
      this.request(path, { method: 'POST', body, signal: ctrl.signal })
        .then((res) => readSSE(res.body!, (e) => sub.next(e), ctrl.signal))
        .then(() => sub.complete())
        .catch((err) => {
          if (!ctrl.signal.aborted) {
            sub.error(err);
          }
        });
      return () => ctrl.abort();
    });
  }

  chat(text: string, image?: File): Observable<ServerEvent> {
    const form = new FormData();
    form.set('text', text);
    if (image) {
      form.set('image', image);
    }
    return this.stream('/api/chat', form);
  }

  approve(actionId: string): Observable<ServerEvent> {
    return this.stream(`/api/actions/${encodeURIComponent(actionId)}/approve`);
  }

  history(): Promise<ChatItem[]> {
    return this.json('/api/messages');
  }

  async reject(actionId: string): Promise<void> {
    await this.request(`/api/actions/${encodeURIComponent(actionId)}/reject`, {
      method: 'POST',
    });
  }

  actions(status: 'PENDING' | ''): Promise<PendingAction[]> {
    return this.json(`/api/actions?status=${status}`);
  }

  invoices(): Promise<Invoice[]> {
    return this.json('/api/invoices');
  }

  summary(period: Period): Promise<Summary[]> {
    return this.json(`/api/summary?period=${period}`);
  }

  refresh(invoiceId: string): Promise<Invoice> {
    return this.json(`/api/invoices/${encodeURIComponent(invoiceId)}/refresh`, {
      method: 'POST',
    });
  }

  events(): Observable<ServerEvent> {
    return new Observable<ServerEvent>((sub) => {
      const source = new EventSource('/api/events/stream');
      for (const type of EVENT_TYPES) {
        source.addEventListener(type, (msg) => {
          try {
            sub.next({ type, data: JSON.parse((msg as MessageEvent).data) } as ServerEvent);
          } catch {
            // Ignore a malformed event.
          }
        });
      }
      return () => source.close();
    });
  }
}
