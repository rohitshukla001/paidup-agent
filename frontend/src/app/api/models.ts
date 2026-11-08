// Shapes from Section 4 of docs/implementation-plan.md.

export interface LineItem {
  name: string;
  quantity: number;
  unit_price: string;
}

export interface InvoiceDraft {
  client_name: string;
  client_email: string;
  currency: string;
  items: LineItem[];
  due_days: number;
  note: string;
}

export interface Invoice {
  id: string;
  number: string;
  status: string;
  client_name: string;
  client_email: string;
  currency: string;
  total: string;
  due_date: string;
  payer_view_url: string;
  updated_at: string;
}

export type ActionStatus = 'PENDING' | 'APPROVED' | 'REJECTED' | 'EXPIRED' | 'DONE' | 'FAILED';

export type Preview =
  | { kind: 'invoice'; draft: InvoiceDraft; total: string; due_date: string }
  | { kind: 'reminder'; invoice: Invoice; subject: string; note: string }
  | { kind: 'cancel'; invoice: Invoice; reason: string };

export interface PendingAction {
  id: string;
  session_id: string;
  tool: string;
  invoice_id: string;
  preview: Preview;
  status: ActionStatus;
  expires_at: string;
}

export interface Summary {
  currency: string;
  paid: string;
  unpaid: string;
  overdue: string;
}

export interface ErrorData {
  code: string;
  message: string;
}

export type ServerEvent =
  | { type: 'message.delta'; data: { text: string } }
  | { type: 'message.done'; data: { message_id: string } }
  | { type: 'action.proposed'; data: PendingAction }
  | { type: 'action.result'; data: { action_id: string; status: ActionStatus; invoice?: Invoice } }
  | { type: 'invoice.updated'; data: Invoice }
  | { type: 'error'; data: ErrorData };

export type Period = 'this_month' | 'last_month' | 'last_30_days' | 'all';

// Thrown when the session cookie is missing or invalid.
export class UnauthorizedError extends Error {
  constructor() {
    super('Log in first.');
  }
}

// Thrown for any other error reply. code comes from the server.
export class ApiError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}
