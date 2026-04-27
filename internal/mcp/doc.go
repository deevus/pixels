// Package mcp implements an MCP server that exposes pixels sandbox lifecycle,
// exec, and file I/O as tools for AI agents.
//
// Concurrency design
//
// This package uses three lock primitives plus two independent primitives.
// Acquisition order (top down; never reverse):
//
//	 1. SandboxLocks.For(name)  — long-held; per-sandbox container ops, including the
//	                               provisioning goroutine and clone-from-base path.
//	 2. Builder.mu              — short-held; protects build state map + failure cache.
//	 3. State.mu                — short-held; protects in-memory state. NEVER held
//	                               across backend I/O.
//
//	INDEPENDENT PRIMITIVES (do not interact with the above):
//
//	  - BuildLock (file flock)  — only acquired inside Builder.DoBuild;
//	                               serialises CLI vs daemon builds of the same base.
//	  - PIDFile                 — held for daemon lifetime.
//
//	INVARIANTS:
//
//	  - sync.Mutex is non-reentrant. Never re-acquire a lock you already hold.
//	  - Reaper.Tick uses TryLock on SandboxLocks. It never blocks.
//	  - The provisioning goroutine acquires SandboxLocks.For(newName) ITSELF
//	    (does not inherit a held lock from the request goroutine), and re-checks
//	    state at the top to handle the destroy-during-create race window.
//	  - Builder.Build dedupes concurrent in-process callers via a buildState map;
//	    cross-process serialization is via BuildLock (flock) inside DoBuild.
//	  - Builder.DoBuild's lock is acquired INSIDE the singleflight-equivalent —
//	    so only one in-process goroutine ever holds the file lock at a time.
package mcp
