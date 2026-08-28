import { boardStore } from './board-store';
import { pdfStore } from './pdf-store';
import { databankStore, type DatabankFile } from './databank-store';
import { ensurePdfPanel, ensureBoardPanel } from './dockview-api';
import { loadBoardWithAscSiblings } from './asc-open';
import { log } from './log-store';
import type { Net } from '../parsers/types';

/**
 * Open one or more PDF files: register, auto-bind, load into pdf.js, create panels.
 * Shared by Toolbar, App (drag-drop), and LibraryPanel to avoid duplicated logic.
 *
 * @param files   - PDF File objects to open
 * @param options - Optional: activeTabId to bind last PDF to, bindAll to bind each individually
 */
export async function openPdfFiles(
  files: (File | { name: string; arrayBuffer(): Promise<ArrayBuffer> })[],
  options?: {
    activeTabId?: number | null;
    /** If true, bind each PDF to activeTabId (used for explicit user action) */
    bindLastToActive?: boolean;
  },
): Promise<void> {
  if (files.length === 0) return;

  const { activeTabId = boardStore.activeTabId, bindLastToActive = true } = options ?? {};

  // Register and auto-bind all PDFs
  for (const file of files) {
    boardStore.addPdf(file as File);
    boardStore.autoBindPdf(file.name);
  }

  // Explicitly bind the last PDF to the active tab (user intent)
  const lastFile = files[files.length - 1];
  if (bindLastToActive && activeTabId !== null && activeTabId !== undefined) {
    boardStore.addPdfBinding(activeTabId, lastFile.name);
  }

  // Load each PDF and create its panel
  for (const file of files) {
    try {
      await pdfStore.loadFile(file as File);
      ensurePdfPanel(file.name);
    } catch (err) {
      log.ui.error(`Failed to load PDF ${file.name}:`, err);
    }
  }

  // Activate the last PDF's panel
  try {
    pdfStore.switchTo(lastFile.name);
    ensurePdfPanel(lastFile.name);
  } catch (err) {
    log.ui.error(`Failed to activate PDF ${lastFile.name}:`, err);
  }
}

/**
 * Load a library board, pulling in the rest of its `.asc` sections when it is
 * one file of a split Tebo-ICT delivery. Shared by the Library panel and the
 * MCP `open_file` tool so a click and a tool call open the same board — and
 * therefore also the single choke point for "a library board just finished
 * parsing" (Block A1 Step 4): both callers land here, so the net-list submit
 * belongs here, not duplicated in each caller.
 *
 * Announces a merge — quietly opening five files when one was clicked would
 * otherwise look like the app guessed at something.
 */
export async function loadLibraryBoard(file: DatabankFile, fileObj: File): Promise<void> {
  const dir = file.path.includes('/') ? file.path.slice(0, file.path.lastIndexOf('/')) : '';
  await loadBoardWithAscSiblings(fileObj, {
    siblingNames: () => databankStore.listFolderNames(dir),
    read: async (name) => {
      const path = dir ? `${dir}/${name}` : name;
      // A sibling need not be indexed — the folder listing sees files the
      // scanner skipped — so synthesise a row when the index has none.
      const row =
        databankStore.fileByPath(path) ??
        ({ ...file, id: -1, path, filename: name, size: 0 } as DatabankFile);
      return databankStore.fetchFileBuffer(row, { quiet: true });
    },
  });
  // Board finished parsing and board-store already has its net registry —
  // hand the list to the backend once (Block A1 Step 4). Not parsed again,
  // not re-sent on every rerender (see submitBoardNetsOnce). `file.id` is
  // the clicked/target file's real databank id (a synthesized -1 id only
  // ever appears on `.asc` *sibling* rows built above, never on `file`
  // itself), so this always targets a real row.
  const parsedBoard = boardStore.activeTab?.board;
  if (parsedBoard) submitBoardNetsOnce(file.id, parsedBoard.nets);
}

/** File ids whose net list has already been sent to the backend this
 *  session. Guards "once per successful parse" (Block A1 Step 4) — a tab can
 *  be re-rendered or the same board re-opened many times without resending
 *  its net list every time. The endpoint is idempotent server-side too (see
 *  docs/assistant/reports/boardripper-contract.md), so a duplicate POST
 *  would be harmless, just wasted traffic; this set avoids sending it. */
const netsSubmitted = new Set<number>();

/** Send a board's already-parsed net-name list to the backend once, right
 *  after a successful parse (Block A1 Step 4 — `POST
 *  /api/databank/files/{id}/nets`). Names are forwarded exactly as the
 *  boardview parser produced them: no case/`_`/`-`/whitespace normalization
 *  happens here — that responsibility stays in repair-kb per
 *  docs/assistant/ARCHITECTURE.md. Best-effort: a failure must never block
 *  or interrupt opening the board, it only leaves the net list unpopulated
 *  for this file until the next successful open. */
function submitBoardNetsOnce(fileId: number, nets: Map<string, Net>): void {
  if (fileId < 0 || netsSubmitted.has(fileId)) return;
  netsSubmitted.add(fileId);
  void fetch(`/api/databank/files/${fileId}/nets`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ nets: Array.from(nets.keys()) }),
  })
    .then((res) => {
      if (!res.ok) {
        netsSubmitted.delete(fileId); // allow a retry on the next open
        log.ui.error(`Failed to submit net list for file ${fileId}: HTTP ${res.status}`);
      }
    })
    .catch((err) => {
      netsSubmitted.delete(fileId);
      log.ui.error(`Failed to submit net list for file ${fileId}:`, err);
    });
}

/** Open a library file (board or PDF) by its databank file id — the
 *  bridge-callable core of LibraryPanel.handleOpenFile, so the MCP `open_file`
 *  tool can bring a library file into the live view. Boards auto-load their
 *  bound (auto_open) schematic PDFs. Returns the opened file's name + type. */
export async function openLibraryFileById(
  fileId: number,
  page?: number,
): Promise<{ name: string; file_type: string }> {
  await databankStore.ensureLoaded();
  // ensureLoaded() no longer blocks on the full file stream, so a miss here
  // isn't authoritative — the row may just not have streamed in yet. Fetch it
  // directly (non-mutating, safe mid-stream) before giving up.
  const file =
    databankStore.fileById(fileId) ?? (await databankStore.fetchFileRows([fileId]))[0];
  if (!file) throw new Error(`file id ${fileId} not in the library index`);
  const fileObj = await databankStore.fetchFileBuffer(file);

  if (file.file_type === 'board') {
    await loadLibraryBoard(file, fileObj);
    const tabId = boardStore.activeTabId;
    // The tab's own name, not the clicked file's — a merged .asc board is
    // named after the board, and the panel title has to agree with it.
    if (tabId != null) ensureBoardPanel(tabId, boardStore.activeTab?.fileName ?? fileObj.name);
    // Auto-load bound (auto_open) PDFs so "open the board" also brings its schematic.
    const detail = await databankStore.fetchFileDetail(file.id);
    for (const binding of detail?.bindings ?? []) {
      if (!binding.auto_open) continue;
      try {
        const pdfFile =
          databankStore.fileById(binding.pdf_file_id) ??
          (await databankStore.fetchFileRows([binding.pdf_file_id]))[0];
        if (!pdfFile) continue;
        const pdfObj = await databankStore.fetchFileBuffer(pdfFile);
        boardStore.addPdf(pdfObj);
        if (tabId != null) boardStore.addPdfBinding(tabId, pdfObj.name);
        await pdfStore.loadFile(pdfObj, pdfFile.id);
        ensurePdfPanel(pdfObj.name);
      } catch (err) {
        log.ui.error('open_file: failed to load bound PDF:', err);
      }
    }
    // Re-activate the board panel so auto-loaded PDFs don't steal focus.
    // The tab's own name, not the clicked file's — a merged .asc board is
    // named after the board, and the panel title has to agree with it.
    if (tabId != null) ensureBoardPanel(tabId, boardStore.activeTab?.fileName ?? fileObj.name);
  } else {
    // PDF (or other) — open via the shared PDF path, then jump to a page.
    await openPdfFiles([fileObj]);
    if (page && page > 0) pdfStore.goToPage(page);
  }
  // Report the board that ended up open — merging a split .asc delivery names
  // the tab after the board, not after the section that was asked for.
  const opened = file.file_type === 'board' ? boardStore.activeTab?.fileName : undefined;
  return { name: opened ?? fileObj.name, file_type: file.file_type };
}

/** Fold case and `_`/`-`/whitespace differences so a URL-typed net name
 *  matches the board's parsed net string. BoardRipper itself does no such
 *  normalization (nothing upstream compares two net-name spellings) — this
 *  is deeplink-local, per ARCHITECTURE.md's normalization rule. */
function normalizeNetKey(name: string): string {
  return name.trim().toUpperCase().replace(/[_\-\s]+/g, '_');
}

/** Resolve a requested net name against a board's net registry. An
 *  unambiguous case/separator-insensitive match wins; a name matching more
 *  than one distinct net spelling is an error, never "take the first". */
function resolveNetName(
  nets: Map<string, Net>,
  requested: string,
):
  | { ok: true; name: string }
  | { ok: false; reason: 'not-found' }
  | { ok: false; reason: 'ambiguous'; candidates: string[] } {
  const target = normalizeNetKey(requested);
  const candidates: string[] = [];
  for (const name of nets.keys()) {
    if (normalizeNetKey(name) === target) candidates.push(name);
  }
  if (candidates.length === 0) return { ok: false, reason: 'not-found' };
  if (candidates.length > 1) return { ok: false, reason: 'ambiguous', candidates };
  return { ok: true, name: candidates[0] };
}

/** Deeplink entry point for `/?board=<board_key>&net=<net_name>` (Block A1
 *  Step 3, see docs/assistant/reports/boardripper-contract.md — Option B).
 *  Resolves `board_key` via the catalog's `manufacturer` field the same way
 *  the databank UI does, opens the board through the existing open-by-id
 *  path, then highlights the net through the existing `highlightNet` — no
 *  duplicated open/highlight logic. A no-op when `board` is absent. Every
 *  failure mode (unknown board, ambiguous boardview file, unknown or
 *  ambiguous net) surfaces as an explicit toast naming what was requested,
 *  never a silent blank screen. */
export async function openDeepLink(search: string): Promise<void> {
  const params = new URLSearchParams(search);
  const boardKey = params.get('board');
  if (!boardKey) return;
  const netName = params.get('net');

  const files = await databankStore.filesByBoardKey(boardKey);
  if (files === null) {
    boardStore.addToast(`Deeplink: could not reach the library backend to resolve board "${boardKey}".`);
    return;
  }
  if (files.length === 0) {
    boardStore.addToast(`Deeplink: no board file found for board "${boardKey}".`);
    return;
  }
  if (files.length > 1) {
    const names = files.map((f) => f.filename).join(', ');
    boardStore.addToast(
      `Deeplink: board "${boardKey}" has ${files.length} boardview files (${names}) — pick one manually.`,
    );
    return;
  }

  let opened: { name: string; file_type: string };
  try {
    opened = await openLibraryFileById(files[0].id);
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    boardStore.addToast(`Deeplink: failed to open board "${boardKey}": ${msg}`);
    return;
  }

  if (!netName) return;

  const board = boardStore.activeTab?.board;
  if (!board) {
    boardStore.addToast(
      `Deeplink: board "${boardKey}" opened as "${opened.name}" but has no net data — cannot highlight net "${netName}".`,
    );
    return;
  }

  const resolved = resolveNetName(board.nets, netName);
  if (!resolved.ok) {
    if (resolved.reason === 'ambiguous') {
      boardStore.addToast(
        `Deeplink: net "${netName}" is ambiguous on board "${boardKey}" — matches ${resolved.candidates.join(', ')}.`,
      );
    } else {
      boardStore.addToast(`Deeplink: net "${netName}" not found on board "${boardKey}".`);
    }
    return;
  }
  boardStore.highlightNet(resolved.name);
}
