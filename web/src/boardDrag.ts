import { useCallback, useEffect, useRef, useState } from 'react';

// Pointer-based dragging for the kanban board.
//
// Underneath this used to be the native HTML5 drag (`draggable`), and that
// feels bad for several reasons: the browser paints a pale ghost copy, there is
// no indication of WHERE the card will land, no auto-scroll, and on touch it
// does not work at all — which is why there was a separate ⋯ menu.
//
// This version uses pointer events (mouse and finger in one): a real floating
// card follows the pointer. The target column highlights for a status change,
// and an insertion line marks the drop when reordering in the same column.

type CardPlacement = { anchorId: string; after: boolean };

export interface DragState {
  rowId: string;
  fromCol: string;
  title: string;
  width: number;
  // current pointer position and grab offset (where the card was taken hold of)
  x: number;
  y: number;
  dx: number;
  dy: number;
  over: string | null; // column under the pointer
  placement: CardPlacement | null;
}

const START_THRESHOLD = 5; // px before a click becomes a drag

// On a finger the rule is different. Five pixels are crossed instantly while
// scrolling — the card stuck to the finger instead of the board scrolling. So:
// hold first, then drag. Anybody who swipes before that scrolls.
const TOUCH_HOLD_MS = 320;
// How much wobble during the hold still counts as "holding". Above that it was
// a swipe, and the drag is never armed in the first place.
const TOUCH_HOLD_SLOP = 10;

function cardPlacement(cards: Element, rowId: string, y: number): CardPlacement | null {
  let last: CardPlacement | null = null;
  for (const card of cards.children) {
    const anchorId = card.getAttribute('data-board-row');
    if (!anchorId || anchorId === rowId) continue;
    const rect = card.getBoundingClientRect();
    if (y < rect.top + rect.height / 2) {
      return card.previousElementSibling?.getAttribute('data-board-row') === rowId
        ? null
        : { anchorId, after: false };
    }
    last = card.nextElementSibling?.getAttribute('data-board-row') === rowId
      ? null
      : { anchorId, after: true };
  }
  return last;
}

export function useBoardDrag(
  onMove: (rowId: string, toCol: string) => void,
  onReorder?: (rowId: string, placement: CardPlacement) => void,
) {
  const [drag, setDrag] = useState<DragState | null>(null);
  // Which card is armed after the hold but has not moved yet. On the iPhone
  // there is no navigator.vibrate — without a visible signal nothing at all
  // happens between the hold and the first movement, and nobody knows whether
  // the card is on the finger now.
  const [armedRow, setArmedRow] = useState<string | null>(null);

  // Live data for the window listeners, so they do not have to be rebound on
  // every position update.
  const live = useRef<{
    rowId: string;
    fromCol: string;
    title: string;
    width: number;
    dx: number;
    dy: number;
    startX: number;
    startY: number;
    pointerId: number;
    board: Element | null;
    started: boolean;
    touch: boolean;
    // armed: on a finger only true after TOUCH_HOLD_MS. Before that every
    // movement belongs to scrolling.
    armed: boolean;
    holdTimer: number | null;
  } | null>(null);
  // Whether a drag really happened — this tells a click (navigate) from a drag
  // (move). A ref, because the pointerup handler needs it straight away.
  const draggedRef = useRef(false);
  const callbacks = useRef({ onMove, onReorder });
  callbacks.current = { onMove, onReorder };

  useEffect(() => {
    const dropUnder = (e: PointerEvent, l: NonNullable<typeof live.current>, scroll = false) => {
      const element = document.elementFromPoint(e.clientX, e.clientY);
      if (!element || element.closest('.board') !== l.board) return { over: null, placement: null };
      const col = element.closest('[data-col]');
      const over = col?.getAttribute('data-col') ?? null;
      const cards = element.closest<HTMLElement>('.board-cards');
      if (scroll && cards) {
        const rect = cards.getBoundingClientRect();
        if (e.clientY - rect.top < 40) cards.scrollTop -= 12;
        else if (rect.bottom - e.clientY < 40) cards.scrollTop += 12;
      }
      const placement = callbacks.current.onReorder && cards && over === l.fromCol
        ? cardPlacement(cards, l.rowId, e.clientY)
        : null;
      return { over, placement };
    };

    const onPointerMove = (e: PointerEvent) => {
      const l = live.current;
      if (!l || l.pointerId !== e.pointerId) return;
      const moved = Math.hypot(e.clientX - l.startX, e.clientY - l.startY);
      if (!l.started) {
        if (l.touch && !l.armed) {
          // Still inside the hold window: whoever swipes wants to scroll. Give
          // the drag up entirely, or the card snaps shut mid-scroll later.
          if (moved > TOUCH_HOLD_SLOP) {
            if (l.holdTimer) clearTimeout(l.holdTimer);
            live.current = null;
            setArmedRow(null);
          }
          return;
        }
        if (moved < START_THRESHOLD) return;
        l.started = true;
        draggedRef.current = true;
      }
      setDrag({
        rowId: l.rowId,
        fromCol: l.fromCol,
        title: l.title,
        width: l.width,
        x: e.clientX,
        y: e.clientY,
        dx: l.dx,
        dy: l.dy,
        ...dropUnder(e, l, true),
      });
    };

    const onPointerUp = (e: PointerEvent) => {
      const l = live.current;
      if (!l || l.pointerId !== e.pointerId) return;
      if (l.holdTimer) clearTimeout(l.holdTimer);
      live.current = null;
      setDrag(null);
      setArmedRow(null);
      if (l.started && e.type !== 'pointercancel') {
        const { over, placement } = dropUnder(e, l);
        if (over && over !== l.fromCol) callbacks.current.onMove(l.rowId, over);
        else if (placement) callbacks.current.onReorder?.(l.rowId, placement);
      }
      // draggedRef stays set until the next click, so the card's click
      // handler can suppress navigation.
    };

    // Once the hold arms a touch drag, the page must not scroll along. That
    // cannot be done through `touch-action`: the value is read when the gesture
    // begins, and changing it later has no effect on the running gesture. So the
    // hard way — passive: false, so preventDefault is allowed.
    const onTouchMove = (e: TouchEvent) => {
      if (live.current?.touch && live.current.armed && e.cancelable) e.preventDefault();
    };

    window.addEventListener('pointermove', onPointerMove);
    window.addEventListener('pointerup', onPointerUp);
    window.addEventListener('pointercancel', onPointerUp);
    window.addEventListener('touchmove', onTouchMove, { passive: false });
    return () => {
      if (live.current?.holdTimer) clearTimeout(live.current.holdTimer);
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerup', onPointerUp);
      window.removeEventListener('pointercancel', onPointerUp);
      window.removeEventListener('touchmove', onTouchMove);
    };
  }, []);

  const startDrag = useCallback(
    (e: React.PointerEvent, rowId: string, fromCol: string, title: string) => {
      // Left mouse button or finger only, and not on a control of the card
      // (the ⋯ menu, a select chip) — there you want to click, not drag.
      if (e.button !== 0 || !e.isPrimary) return;
      if ((e.target as Element).closest?.('.card-move, .card-prop-edit, a, button')) return;
      const card = (e.currentTarget as HTMLElement).getBoundingClientRect();
      draggedRef.current = false;
      const touch = e.pointerType === 'touch' || e.pointerType === 'pen';
      live.current = {
        rowId,
        fromCol,
        title,
        width: card.width,
        dx: e.clientX - card.left,
        dy: e.clientY - card.top,
        startX: e.clientX,
        startY: e.clientY,
        pointerId: e.pointerId,
        board: e.currentTarget.closest('.board'),
        started: false,
        touch,
        armed: !touch, // mouse: armed at once. Finger: only after the hold.
        holdTimer: null,
      };
      if (touch) {
        const l = live.current;
        l.holdTimer = window.setTimeout(() => {
          if (live.current !== l) return;
          l.armed = true;
          setArmedRow(l.rowId);
          navigator.vibrate?.(12);
        }, TOUCH_HOLD_MS);
      }
    },
    [],
  );

  // The card click asks this: if a drag just happened, do NOT navigate.
  const consumeClick = useCallback(() => {
    if (draggedRef.current) {
      draggedRef.current = false;
      return true; // Klick gehoerte zu einem Ziehen — schlucken
    }
    return false;
  }, []);

  return { drag, armedRow, startDrag, consumeClick };
}
