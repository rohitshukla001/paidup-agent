import { Component, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { Router } from '@angular/router';
import { environment } from '../../environments/environment';
import { Backend } from '../api/backend';

@Component({
  selector: 'app-login',
  imports: [FormsModule],
  template: `
    <main class="login">
      <form class="panel login-box" (ngSubmit)="submit()">
        <h1 class="brand">PaidUp</h1>
        <p class="muted">Get paid for freelance work without writing invoices by hand.</p>
        <label for="password">Demo password</label>
        <input
          id="password"
          name="password"
          type="password"
          autocomplete="current-password"
          [(ngModel)]="password"
          required
          autofocus
        />
        @if (error()) {
          <p class="error" role="alert">{{ error() }}</p>
        }
        <button class="primary" type="submit" [disabled]="busy() || !password">
          {{ busy() ? 'Logging in...' : 'Log in' }}
        </button>
        @if (mock) {
          <p class="muted small">Mock mode: the password is "demo".</p>
        }
      </form>
    </main>
  `,
})
export class Login {
  private api = inject(Backend);
  private router = inject(Router);
  protected readonly mock = environment.mock;
  protected password = '';
  protected readonly busy = signal(false);
  protected readonly error = signal('');

  async submit(): Promise<void> {
    this.busy.set(true);
    this.error.set('');
    try {
      await this.api.login(this.password);
      await this.router.navigateByUrl('/');
    } catch (err) {
      this.error.set(err instanceof Error ? err.message : 'Login failed.');
    } finally {
      this.busy.set(false);
    }
  }
}
