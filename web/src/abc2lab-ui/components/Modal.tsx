import type { ReactNode } from 'react';
import { useBodyScrollLock, useEscape } from '../lib/hooks';

type ModalProps = {
  open: boolean;
  onClose: () => void;
  /** .rpop 또는 .vpop 같은 팝업 본체 */
  children: ReactNode;
};

/** 원본 .modal: 배경 클릭 · ESC로 닫히고, 열려 있는 동안 배경 스크롤을 잠가요 */
export function Modal({ open, onClose, children }: ModalProps) {
  useBodyScrollLock(open);
  useEscape(open, onClose);
  if (!open) return null;
  return (
    <div className="modal">
      <div className="modal-back" onClick={onClose} />
      {children}
    </div>
  );
}
