import { Component, OnDestroy, OnInit, inject } from '@angular/core';
import { environment } from '../environments/environment';
import { Chat } from './chat/chat';
import { Dashboard } from './dashboard/dashboard';
import { AppState } from './state';

@Component({
  selector: 'app-home',
  imports: [Chat, Dashboard],
  template: `
    <header class="topbar">
      <span class="brand">PaidUp</span>
      <span class="muted small">
        PayPal sandbox{{ mock ? ' (mock data)' : '' }}. Nothing is sent without your approval.
      </span>
    </header>
    <main class="layout">
      <app-chat />
      <app-dashboard />
    </main>
  `,
})
export class Home implements OnInit, OnDestroy {
  private readonly state = inject(AppState);
  protected readonly mock = environment.mock;

  ngOnInit(): void {
    this.state.start();
  }

  ngOnDestroy(): void {
    this.state.stop();
  }
}
