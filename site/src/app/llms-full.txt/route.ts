import { docsLlms } from '@/lib/source';

export const dynamic = "force-static";

export const revalidate = false;

export async function GET() {
  return new Response(await docsLlms.full());
}
