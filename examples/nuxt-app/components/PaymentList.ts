import { formatMoney } from '../composables/useMoney';
import type { Payment } from '~/types/payment';

// A component's logic, without the template, so the example needs no Vue
// toolchain to be indexed.
export function renderRows(payments: Payment[]): string[] {
  return payments.map((p) => `${p.id}: ${formatMoney(p.amount)} (${p.status})`);
}
