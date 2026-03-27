import type { APIErrorResponse } from '$lib/types';

const API_BASE = '/_oberwatch/api/v1';

export class ApiError extends Error {
  status: number;
  details?: APIErrorResponse;

  constructor(response: Response, details?: APIErrorResponse) {
    super(details?.error?.message ?? `Request failed with status ${response.status}`);
    this.name = 'ApiError';
    this.status = response.status;
    this.details = details;
  }
}

export async function fetchJSON<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`, {
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers ?? {})
    },
    ...options
  });

  if (!response.ok) {
    let details: APIErrorResponse | undefined;
    try {
      details = (await response.json()) as APIErrorResponse;
    } catch {
      details = undefined;
    }
    throw new ApiError(response, details);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  const contentType = response.headers.get('content-type') ?? '';
  if (!contentType.includes('application/json')) {
    return undefined as T;
  }

  const body = await response.text();
  if (!body) {
    return undefined as T;
  }

  return JSON.parse(body) as T;
}

export async function fetchBlob(path: string, options: RequestInit = {}): Promise<Blob> {
  const response = await fetch(`${API_BASE}${path}`, {
    credentials: 'same-origin',
    headers: {
      ...(options.headers ?? {})
    },
    ...options
  });

  if (!response.ok) {
    let details: APIErrorResponse | undefined;
    try {
      details = (await response.json()) as APIErrorResponse;
    } catch {
      details = undefined;
    }
    throw new ApiError(response, details);
  }

  return response.blob();
}
