import { type ReactNode, useEffect, useRef } from "react";

export function Dialog({
  labelledBy,
  className,
  onClose,
  initialFocusSelector = ".detail-close",
  returnFocusFallback,
  children,
}: {
  labelledBy: string;
  className?: string;
  onClose: () => void;
  initialFocusSelector?: string;
  returnFocusFallback?: () => HTMLElement | null;
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
      document.activeElement instanceof HTMLElement
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
    dialog.addEventListener("cancel", handleCancel);

    return () => {
      dialog.removeEventListener("cancel", handleCancel);
      document.body.style.overflow = previousOverflow;
      if (dialog.open) dialog.close();
      const target = opener?.isConnected ? opener : fallbackRef.current?.();
      target?.focus();
    };
  }, [initialFocusSelector]);

  return (
    <dialog
      aria-labelledby={labelledBy}
      className={className}
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
