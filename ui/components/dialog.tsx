import { type ReactNode, useEffect, useRef } from "react";
import "./dialog.css";

export function Dialog({
  labelledBy,
  className,
  onClose,
  initialFocusSelector = ".detail-close",
  returnFocusFallback,
  preferFallback = false,
  variant = "modal",
  children,
}: {
  labelledBy: string;
  className?: string;
  onClose: () => void;
  initialFocusSelector?: string;
  returnFocusFallback?: () => HTMLElement | null;
  // Content can change while open, so the opener may no longer be the right target.
  preferFallback?: boolean;
  variant?: "modal" | "drawer";
  children: ReactNode;
}) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const fallbackRef = useRef(returnFocusFallback);
  fallbackRef.current = returnFocusFallback;

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    const opener =
      document.activeElement instanceof HTMLElement &&
      document.activeElement !== document.body
        ? document.activeElement
        : null;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    if (!dialog.open) dialog.showModal();
    dialog.querySelector<HTMLElement>(initialFocusSelector)?.focus();

    // Native cancel fires on Escape regardless of where focus sits.
    const handleCancel = (event: Event) => {
      event.preventDefault();
      onCloseRef.current();
    };
    // Browsers may force-close after repeated prevented cancels; keep state in sync.
    const handleClose = () => onCloseRef.current();
    dialog.addEventListener("cancel", handleCancel);
    dialog.addEventListener("close", handleClose);

    return () => {
      dialog.removeEventListener("cancel", handleCancel);
      dialog.removeEventListener("close", handleClose);
      document.body.style.overflow = previousOverflow;
      if (dialog.open) dialog.close();
      const fallback = fallbackRef.current?.();
      const connectedOpener = opener?.isConnected ? opener : null;
      const target = preferFallback
        ? (fallback ?? connectedOpener)
        : (connectedOpener ?? fallback);
      target?.focus();
    };
  }, [initialFocusSelector, preferFallback]);

  return (
    <dialog
      aria-labelledby={labelledBy}
      className={
        variant === "drawer"
          ? `${className ?? ""} dialog-drawer`.trim()
          : className
      }
      onMouseDown={(event) => {
        if (event.button === 0 && event.target === event.currentTarget) {
          onClose();
        }
      }}
      ref={dialogRef}
    >
      {children}
    </dialog>
  );
}
