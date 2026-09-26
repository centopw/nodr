import type { ButtonHTMLAttributes } from "react";

export type ButtonVariant = "primary" | "secondary" | "danger";
export type ButtonSize = "default" | "small";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
}

const variantClass: Record<ButtonVariant, string> = {
  primary: "",
  secondary: "btn-secondary",
  danger: "btn-danger",
};

export default function Button({
  variant = "primary",
  size = "default",
  className,
  type = "button",
  ...rest
}: ButtonProps) {
  const classes = [
    size === "small" ? "action-btn" : "",
    variantClass[variant],
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");

  return <button type={type} className={classes || undefined} {...rest} />;
}
