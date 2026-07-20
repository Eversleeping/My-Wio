import type { ComponentType, ReactNode } from "react";

export interface DialogSlotProps {
  open: boolean;
  title: string;
  onClose: () => void;
  children: ReactNode;
  wide?: boolean;
}

export interface FieldSlotProps {
  label: string;
  children: ReactNode;
}

export interface DialogActionsSlotProps {
  children: ReactNode;
}

export interface DataTableSlotProps {
  headers: string[];
  empty: string;
  children: ReactNode;
}

export interface StatusSlotProps {
  value: string;
  icon?: ReactNode;
}

export interface CreateProjectDialogSlots {
  Dialog: ComponentType<DialogSlotProps>;
  Field: ComponentType<FieldSlotProps>;
  DialogActions: ComponentType<DialogActionsSlotProps>;
}

export interface ProjectTableSlots {
  DataTable: ComponentType<DataTableSlotProps>;
  Status: ComponentType<StatusSlotProps>;
}
