import { Component, ElementRef, computed, effect, inject, input, output, signal, viewChild } from '@angular/core';
import { Period } from '../api/models';

export const PERIODS: { value: Period; label: string }[] = [
  { value: 'this_month', label: 'This month' },
  { value: 'last_month', label: 'Last month' },
  { value: 'last_30_days', label: 'Last 30 days' },
  { value: 'all', label: 'All time' },
];

// PeriodMenu is a dropdown for the paid period. It follows the listbox
// pattern, so it works with a mouse, a keyboard, and a screen reader.
@Component({
  selector: 'app-period-menu',
  host: {
    class: 'period-menu',
    '(document:click)': 'onDocumentClick($event)',
  },
  template: `
    <button
      #trigger
      type="button"
      class="period-trigger"
      aria-haspopup="listbox"
      [attr.aria-expanded]="open()"
      aria-controls="period-list"
      [attr.aria-label]="'Period for paid money: ' + selectedLabel()"
      (click)="toggle()"
      (keydown)="onTriggerKey($event)"
    >
      <span>{{ selectedLabel() }}</span>
      <svg class="chevron" [class.up]="open()" viewBox="0 0 20 20" aria-hidden="true">
        <path d="M5.5 7.5 10 12l4.5-4.5" />
      </svg>
    </button>
    @if (open()) {
      <ul
        #list
        id="period-list"
        class="period-list"
        role="listbox"
        tabindex="-1"
        aria-label="Period for paid money"
        [attr.aria-activedescendant]="'period-' + periods[active()].value"
        (keydown)="onListKey($event)"
      >
        @for (p of periods; track p.value; let i = $index) {
          <li
            [id]="'period-' + p.value"
            role="option"
            [attr.aria-selected]="p.value === value()"
            [class.active]="i === active()"
            (mouseenter)="active.set(i)"
            (click)="choose(p.value)"
          >
            <span>{{ p.label }}</span>
            @if (p.value === value()) {
              <svg class="check" viewBox="0 0 20 20" aria-hidden="true">
                <path d="M4.5 10.5 8.5 14.5 15.5 6" />
              </svg>
            }
          </li>
        }
      </ul>
    }
  `,
})
export class PeriodMenu {
  readonly value = input.required<Period>();
  readonly changed = output<Period>();

  protected readonly periods = PERIODS;
  protected readonly open = signal(false);
  protected readonly active = signal(0);
  protected readonly selectedLabel = computed(
    () => PERIODS.find((p) => p.value === this.value())?.label ?? '',
  );

  private readonly host = inject(ElementRef<HTMLElement>);
  private readonly trigger = viewChild<ElementRef<HTMLButtonElement>>('trigger');
  private readonly list = viewChild<ElementRef<HTMLUListElement>>('list');

  constructor() {
    // Move the focus into the list once Angular has rendered it.
    effect(() => {
      if (this.open()) {
        this.list()?.nativeElement.focus();
      }
    });
  }

  protected toggle(): void {
    if (this.open()) {
      this.close(false);
    } else {
      this.show();
    }
  }

  protected onTriggerKey(e: KeyboardEvent): void {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      this.show();
    }
  }

  protected onListKey(e: KeyboardEvent): void {
    const last = PERIODS.length - 1;
    switch (e.key) {
      case 'ArrowDown':
        this.active.set(Math.min(this.active() + 1, last));
        break;
      case 'ArrowUp':
        this.active.set(Math.max(this.active() - 1, 0));
        break;
      case 'Home':
        this.active.set(0);
        break;
      case 'End':
        this.active.set(last);
        break;
      case 'Enter':
      case ' ':
        this.choose(PERIODS[this.active()].value);
        break;
      case 'Escape':
        this.close(true);
        break;
      case 'Tab':
        this.close(false);
        return; // let Tab move the focus as usual
      default:
        return;
    }
    e.preventDefault();
  }

  protected choose(p: Period): void {
    if (p !== this.value()) {
      this.changed.emit(p);
    }
    this.close(true);
  }

  protected onDocumentClick(e: MouseEvent): void {
    if (this.open() && !this.host.nativeElement.contains(e.target as Node)) {
      this.close(false);
    }
  }

  private show(): void {
    this.active.set(Math.max(0, PERIODS.findIndex((p) => p.value === this.value())));
    this.open.set(true);
  }

  private close(refocus: boolean): void {
    this.open.set(false);
    if (refocus) {
      this.trigger()?.nativeElement.focus();
    }
  }
}
