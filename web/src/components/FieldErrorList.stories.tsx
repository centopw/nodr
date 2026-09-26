import type { Meta, StoryObj } from "@storybook/react-vite";
import FieldErrorList from "./FieldErrorList";

const meta = {
  title: "Components/FieldErrorList",
  component: FieldErrorList,
  tags: ["autodocs"],
} satisfies Meta<typeof FieldErrorList>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Empty: Story = {
  args: { errors: [] },
};

export const SingleError: Story = {
  args: {
    errors: [{ path: "name", message: "Name is required" }],
  },
};

export const MultipleErrors: Story = {
  args: {
    errors: [
      { path: "name", message: "Name is required" },
      { path: "cluster", message: "Cluster does not exist" },
      { path: "storage", message: "Storage pool is full" },
    ],
  },
};
