import { MemoryPaymentRepository } from '../repository';
import { formatMoney } from '../../composables/useMoney';
import type { Payment } from '~/types/payment';

const repository = new MemoryPaymentRepository();

// A route handler. The API base URL is configuration, which is what links this
// file to the compose service and the Dockerfile in the graph.
const apiBase = process.env.NUXT_PUBLIC_API_BASE ?? 'http://localhost:3000';

export default async function handler(): Promise<Array<Payment & { display: string }>> {
  const payments = await repository.list('demo-customer');
  return payments.map((p) => ({ ...p, display: formatMoney(p.amount) }));
}

export { apiBase };
