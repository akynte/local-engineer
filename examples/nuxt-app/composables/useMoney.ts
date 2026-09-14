import type { Money } from '~/types/payment';

// Presentation only. It is called from the route handler and from a component,
// so it is a consumer the graph should find when Money changes.
export function formatMoney(amount: Money): string {
  const major = (amount.minor / 100).toFixed(2);
  return `${major} ${amount.currency}`;
}

export function isZero(amount: Money): boolean {
  return amount.minor === 0;
}
