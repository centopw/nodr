import type { ReactNode } from "react";

export interface ModalProps {
  open: boolean;
  onClose: () => void;
  closeDisabled?: boolean;
  titleId: string;
  children: ReactNode;
}

export default function Modal({
  open,
  onClose,
  closeDisabled = false,
  titleId,
  children,
}: ModalProps) {
  if (!open) {
    return null;
  }

  return (
    <div
      className="modal-backdrop"
      role="presentation"
      onClick={(e) => {
        if (e.target === e.currentTarget && !closeDisabled) {
          onClose();
        }
      }}
    >
      <div
        className="modal-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
      >
        {children}
      </div>
    </div>
  );
}
