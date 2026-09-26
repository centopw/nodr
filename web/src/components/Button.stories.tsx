import type { Meta, StoryObj } from "@storybook/react-vite";
import Button from "./Button";

const meta = {
  title: "Components/Button",
  component: Button,
  tags: ["autodocs"],
  argTypes: {
    variant: {
      control: "select",
      options: ["primary", "secondary", "danger"],
    },
    size: {
      control: "select",
      options: ["default", "small"],
    },
  },
  args: {
    children: "Button",
    onClick: () => {},
  },
} satisfies Meta<typeof Button>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Primary: Story = {
  args: { variant: "primary" },
};

export const Secondary: Story = {
  args: { variant: "secondary" },
};

export const Danger: Story = {
  args: { variant: "danger" },
};

export const Small: Story = {
  args: { variant: "primary", size: "small" },
};

export const SecondarySmall: Story = {
  args: { variant: "secondary", size: "small" },
};

export const DangerSmall: Story = {
  args: { variant: "danger", size: "small" },
};

export const Disabled: Story = {
  args: { variant: "primary", disabled: true },
};
