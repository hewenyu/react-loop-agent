// Numbers and strings the renderer shares with the daemon, or tunes in one
// place.

// How long a request to the daemon may take.
export const REQUEST_TIMEOUT_MS = 10_000;

// The state stream reconnects after 250ms × 2^n, n capped so the wait
// stays under 20s.
export const EVENTS_BACKOFF_BASE_MS = 250;
export const EVENTS_BACKOFF_MAX_STEP = 6;

// A terminal stream that dropped reconnects after this long.
export const TERM_RETRY_MS = 1500;
// Terminal resizes are sent once the window stops moving.
export const TERM_RESIZE_DEBOUNCE_MS = 60;
export const TERM_SCROLLBACK = 20_000;

// How the daemon closes a terminal stream (internal/api term): 4000 means
// the viewer fell behind and should replay; "exited" means the program
// ended and there is nothing more to follow.
export const TERM_CLOSE_RESYNC = 4000;
export const TERM_CLOSE_EXITED = "exited";

// Keys for Claude's permission prompt, which has "Yes" selected.
export const KEY_ENTER = "\r";
export const KEY_ESC = "\x1b";

// The daemon's detail on a blocked agent asking for permission
// (internal/agent/status.go).
export const PERMISSION_DETAIL_PREFIX = "需要授权";

// How many ended things to keep on screen.
export const SIDEBAR_ENDED_AGENTS = 8;
export const BOARD_ENDED_TASKS = 40;

// Bounds of a repo's concurrency in the repos view.
export const REPO_CONCURRENCY_MIN = 1;
export const REPO_CONCURRENCY_MAX = 8;

export const NOTIFY_BODY_MAX = 200;
