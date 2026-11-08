import { Observable } from 'rxjs';
import { Invoice, PendingAction, Period, ServerEvent, Summary } from './models';

// Backend is the API the app uses. HttpBackend talks to the Go server;
// MockBackend runs in the browser with fake data.
export abstract class Backend {
  abstract session(): Promise<boolean>;
  abstract login(password: string): Promise<void>;
  abstract chat(text: string, image?: File): Observable<ServerEvent>;
  abstract approve(actionId: string): Observable<ServerEvent>;
  abstract reject(actionId: string): Promise<void>;
  abstract actions(status: 'PENDING' | ''): Promise<PendingAction[]>;
  abstract invoices(): Promise<Invoice[]>;
  abstract summary(period: Period): Promise<Summary[]>;
  abstract refresh(invoiceId: string): Promise<Invoice>;
  // Live dashboard events. The stream reconnects by itself.
  abstract events(): Observable<ServerEvent>;
}
