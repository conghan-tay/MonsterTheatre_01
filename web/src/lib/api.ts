// A thin, typed wrapper over fetch for the Go API. Every call is same-origin
// (see next.config.ts), so the session cookie is sent automatically.

export type Scene = { topic: string; description: string; genre: string };

export type Character = { name: string; brief: string; portraitUrl: string };

export type Room = {
  roomId: string;
  scene: Scene;
  characterA: Character;
  characterB: Character;
  shareLink: string;
  audienceCount: number;
  createdAt: string;
};

// What an audience member is shown of a room: no share link and no count.
export type RoomView = Pick<Room, "roomId" | "scene" | "characterA" | "characterB">;

export type Entered = { sessionId: string; roomId: string; restored: boolean; room: RoomView };

export type Performance = { performanceId: string; roomId: string; status: "running" | "completed" | "failed" };

export type RoomSummary = { roomId: string; topic: string; createdAt: string };

export type Director = { directorId: string; username: string };

// ApiError carries the server's error shape:
// {"error": {"code", "message", "fields"?}}
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, { credentials: "same-origin", ...init });
  } catch {
    throw new ApiError(0, "network_error", "Could not reach the server. Check your connection and try again.");
  }
  if (response.status === 204) return undefined as T;

  const text = await response.text();
  let body: unknown = undefined;
  try {
    body = text ? JSON.parse(text) : undefined;
  } catch {
    // Not JSON: fall through to the generic error below.
  }

  if (!response.ok) {
    const error = (body as { error?: { code?: string; message?: string; fields?: Record<string, string> } })?.error;
    throw new ApiError(
      response.status,
      error?.code ?? "unexpected_error",
      error?.message ?? `The server answered ${response.status}.`,
      error?.fields,
    );
  }
  return body as T;
}

function json(body: unknown): RequestInit {
  return { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
}

export const api = {
  login: (username: string, password: string) =>
    request<Director>("/api/v1/director/login", json({ username, password })),
  logout: () => request<void>("/api/v1/director/logout", json({})),
  me: () => request<Director>("/api/v1/director/me"),
  listRooms: () => request<{ rooms: RoomSummary[] }>("/api/v1/rooms").then((r) => r.rooms),
  getRoom: (roomId: string) => request<Room>(`/api/v1/rooms/${encodeURIComponent(roomId)}`),
  // The browser sets the multipart boundary itself, so no Content-Type here.
  createRoom: (form: FormData) => request<Room>("/api/v1/rooms", { method: "POST", body: form }),
  // The Enter theatre click. Safe to repeat: a browser that already has a
  // session for the room gets it back, and no seat is used.
  join: (roomId: string, token: string) => request<Entered>("/api/v1/audience-sessions", json({ roomId, token })),
  // The Start button. Safe to repeat: a room has one performance.
  start: (roomId: string) =>
    request<Performance>(`/api/v1/rooms/${encodeURIComponent(roomId)}/performance`, json({})),
};
