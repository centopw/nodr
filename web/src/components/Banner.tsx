import type { ReactNode } from "react";

export type BannerVariant = "error" | "success" | "warning";

export interface BannerProps {
  variant: BannerVariant;
  role?: "alert" | "status";
  className?: string;
  children: ReactNode;
}

const defaultRole: Record<BannerVariant, "alert" | "status"> = {
  error: "alert",
  success: "status",
  warning: "alert",
};

export default function Banner({ variant, role, className, children }: BannerProps) {
  const classes = ["message", `message-${variant}`, className ?? ""]
    .filter(Boolean)
    .join(" ");

  return (
    <div className={classes} role={role ?? defaultRole[variant]}>
      {children}
    </div>
  );
}
