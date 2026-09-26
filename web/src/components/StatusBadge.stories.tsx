import type { Meta, StoryObj } from "@storybook/react-vite";
import StatusBadge from "./StatusBadge";

const meta = {
  title: "Components/StatusBadge",
  component: StatusBadge,
  tags: ["autodocs"],
} satisfies Meta<typeof StatusBadge>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Running: Story = {
  args: { powerState: "running" },
};

export const Stopped: Story = {
  args: { powerState: "stopped" },
};

export const Unmanaged: Story = {
  args: { powerState: "unmanaged" },
};

export const UnknownState: Story = {
  args: { powerState: "provisioning" },
};
