import { Pipe, PipeTransform } from '@angular/core';

const formats = new Map<string, Intl.NumberFormat>();

// money formats an amount like a finance app: "$1,580.00", "€90.00",
// "CA$12.50". It falls back to "1580.00 XYZ" for a code that the browser
// does not know.
export function money(amount: string | number, currency: string): string {
  const n = typeof amount === 'number' ? amount : Number(amount);
  if (!Number.isFinite(n) || !currency) {
    return `${amount} ${currency}`.trim();
  }
  let f = formats.get(currency);
  if (!f) {
    try {
      f = new Intl.NumberFormat('en-US', { style: 'currency', currency });
    } catch {
      return `${amount} ${currency}`;
    }
    formats.set(currency, f);
  }
  return f.format(n);
}

// isZero reports whether an amount string is zero, such as "0.00".
export function isZero(amount: string): boolean {
  return Number(amount) === 0;
}

@Pipe({ name: 'money' })
export class MoneyPipe implements PipeTransform {
  transform(amount: string | number, currency: string): string {
    return money(amount, currency);
  }
}
