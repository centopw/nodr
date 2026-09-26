import type { Meta, StoryObj } from "@storybook/react-vite";
import Banner from "./Banner";

const meta = {
  title: "Components/Banner",
  component: Banner,
  tags: ["autodocs"],
  argTypes: {
    variant: {
      control: "select",
      options: ["error", "success", "warning"],
    },
  },
} satisfies Meta<typeof Banner>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Error: Story = {
  args: {
    variant: "error",
    children: "Unable to load nodr.",
  },
};

export const Success: Story = {
  args: {
    variant: "success",
    children: "Changes applied successfully.",
  },
};

export const Warning: Story = {
  args: {
    variant: "warning",
    children:
      "This plan contains destructive changes that will replace or destroy resources.",
  },
};
