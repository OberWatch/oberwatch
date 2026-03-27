import type { APIErrorResponse } from '$lib/types';

const API_BASE = '/_oberwatch/api/v1';

function getToken(): string {
  if (typeof localStorage === 'undefined') {
    return '';
  }
  return localStorage.getItem('oberwatch_admin_token') ?? '';
}

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
    headers: {
      Authorization: `Bearer ${getToken()}`,
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

  return (await response.json()) as T;
}
