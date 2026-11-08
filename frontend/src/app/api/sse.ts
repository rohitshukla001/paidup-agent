import { ServerEvent } from './models';

// parseBlock reads one SSE block ("event: x\ndata: {...}"). Comment
// lines (": ping") and blocks without data give null.
export function parseBlock(block: string): ServerEvent | null {
  let type = 'message';
  const data: string[] = [];
  for (const line of block.split('\n')) {
    if (line.startsWith('event:')) {
      type = line.slice(6).trim();
    } else if (line.startsWith('data:')) {
      data.push(line.slice(5).trimStart());
    }
  }
  if (data.length === 0) {
    return null;
  }
  try {
    return { type, data: JSON.parse(data.join('\n')) } as ServerEvent;
  } catch {
    return null;
  }
}

// readSSE reads a fetch() response body as Server-Sent Events. The
// browser EventSource API supports only GET, and the chat and approve
// streams are POST requests.
export async function readSSE(
  body: ReadableStream<Uint8Array>,
  onEvent: (e: ServerEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  try {
    while (!signal?.aborted) {
      const { value, done } = await reader.read();
      if (done) {
        break;
      }
      buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n');
      let end = buffer.indexOf('\n\n');
      while (end >= 0) {
        const event = parseBlock(buffer.slice(0, end));
        buffer = buffer.slice(end + 2);
        if (event) {
          onEvent(event);
        }
        end = buffer.indexOf('\n\n');
      }
    }
    const last = parseBlock(buffer.trim());
    if (last) {
      onEvent(last);
    }
  } finally {
    reader.releaseLock();
  }
}
