import { Component, ElementRef, effect, inject, signal, viewChild } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { ApprovalCard } from '../card/approval-card';
import { AppState } from '../state';

const MAX_IMAGE = 5 * 1024 * 1024;
const IMAGE_TYPES = ['image/jpeg', 'image/png', 'image/webp'];

@Component({
  selector: 'app-chat',
  imports: [FormsModule, ApprovalCard],
  template: `
    <section class="panel chat" aria-labelledby="chat-title">
      <h2 id="chat-title">Assistant</h2>
      <div class="messages" #list aria-live="polite">
        @if (state.messages().length === 0) {
          <div class="empty">
            <p>Describe a job or attach a photo of a quote. For example:</p>
            <button type="button" class="example" (click)="useExample(example1)">
              {{ example1 }}
            </button>
            <button type="button" class="example" (click)="useExample(example2)">
              {{ example2 }}
            </button>
            <button type="button" class="example sample-photo" (click)="useSamplePhoto()"
                    [disabled]="state.sending()">
              <svg viewBox="0 0 24 24" aria-hidden="true">
                <path d="M4 8h3l2-3h6l2 3h3v11H4z" />
                <circle cx="12" cy="13" r="3.5" />
              </svg>
              Try a sample photo of a handwritten quote
            </button>
          </div>
        }
        @for (m of state.messages(); track m.id) {
          @switch (m.role) {
            @case ('user') {
              <div class="bubble user">@if (m.imageUrl) {<img class="photo" [src]="m.imageUrl" [alt]="'Photo: ' + m.imageName" />} @else if (m.imageName) {<span class="attachment">Photo attached</span>}@if (m.text) {<span>{{ m.text }}</span>}</div>
            }
            @case ('tool') {
              <div class="tool-line">
                <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M4.5 10.5 8.5 14.5 15.5 6" /></svg>
                {{ m.text }}
              </div>
            }
            @case ('assistant') {
              <div class="bubble assistant">{{ m.text }}</div>
            }
            @case ('error') {
              <div class="bubble error" role="alert">{{ m.text }}</div>
            }
            @case ('card') {
              @if (state.cards()[m.cardId!]; as card) {
                <app-approval-card [card]="card" />
              }
            }
          }
        }
        @if (state.sending()) {
          <div class="bubble assistant typing" aria-label="The assistant is working">...</div>
        }
      </div>

      <form class="composer" (ngSubmit)="send()">
        @if (image()) {
          <div class="chip">
            Photo: {{ image()!.name }}
            <button type="button" class="link" (click)="clearImage()" aria-label="Remove photo">
              Remove
            </button>
          </div>
        }
        @if (fileError()) {
          <p class="error" role="alert">{{ fileError() }}</p>
        }
        <label class="sr-only" for="message">Message</label>
        <textarea
          id="message"
          name="message"
          rows="2"
          [(ngModel)]="text"
          (keydown.enter)="onEnter($event)"
          placeholder="Describe a job, or ask about your invoices"
        ></textarea>
        <div class="composer-row">
          <label class="button" for="photo">Attach photo</label>
          <button type="button" class="link sample-link" (click)="useSamplePhoto()"
                  [disabled]="state.sending()">Try a sample photo</button>
          <input id="photo" class="sr-only" type="file" accept="image/jpeg,image/png,image/webp"
                 (change)="pick($event)" #file />
          <button class="primary" type="submit"
                  [disabled]="state.sending() || (!text.trim() && !image())">Send</button>
        </div>
      </form>
    </section>
  `,
})
export class Chat {
  protected readonly state = inject(AppState);
  protected readonly example1 =
    'Bill John at john@example.com 450 USD for a landing page, due in 7 days.';
  protected readonly example2 = 'Who owes me money?';
  protected text = '';
  protected readonly image = signal<File | undefined>(undefined);
  protected readonly fileError = signal('');
  private readonly list = viewChild<ElementRef<HTMLElement>>('list');
  private readonly file = viewChild<ElementRef<HTMLInputElement>>('file');

  constructor() {
    // Keep the newest message in view.
    effect(() => {
      this.state.messages();
      this.state.sending();
      const el = this.list()?.nativeElement;
      if (el) {
        queueMicrotask(() => (el.scrollTop = el.scrollHeight));
      }
    });
  }

  protected useExample(text: string): void {
    this.text = text;
  }

  // useSamplePhoto sends the sample quote that ships with the app, so a
  // judge can try photo reading without a file of their own.
  protected async useSamplePhoto(): Promise<void> {
    if (this.state.sending()) {
      return;
    }
    this.fileError.set('');
    try {
      const res = await fetch('samples/handwritten-quote.png');
      if (!res.ok) {
        throw new Error(String(res.status));
      }
      const blob = await res.blob();
      this.state.send('', new File([blob], 'handwritten-quote.png', { type: 'image/png' }));
    } catch {
      this.fileError.set('The sample photo could not load. Try again.');
    }
  }

  protected onEnter(e: Event): void {
    const k = e as KeyboardEvent;
    if (!k.shiftKey && !k.isComposing) {
      k.preventDefault();
      this.send();
    }
  }

  protected pick(e: Event): void {
    const f = (e.target as HTMLInputElement).files?.[0];
    this.fileError.set('');
    if (!f) {
      return;
    }
    if (!IMAGE_TYPES.includes(f.type)) {
      this.fileError.set('Attach a JPEG, PNG, or WebP photo.');
    } else if (f.size > MAX_IMAGE) {
      this.fileError.set('The photo is too large. The limit is 5 MB.');
    } else {
      this.image.set(f);
      return;
    }
    this.clearImage();
  }

  protected clearImage(): void {
    this.image.set(undefined);
    const input = this.file()?.nativeElement;
    if (input) {
      input.value = '';
    }
  }

  protected send(): void {
    const text = this.text.trim();
    if (this.state.sending() || (!text && !this.image())) {
      return;
    }
    this.state.send(text, this.image());
    this.text = '';
    this.clearImage();
  }
}
