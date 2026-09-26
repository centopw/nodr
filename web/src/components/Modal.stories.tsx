import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import Modal from "./Modal";
import Button from "./Button";

const meta = {
  title: "Components/Modal",
  component: Modal,
  tags: ["autodocs"],
} satisfies Meta<typeof Modal>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Open: Story = {
  args: {
    open: true,
    onClose: () => {},
    titleId: "modal-open-title",
    children: (
      <>
        <h3 id="modal-open-title">Confirm deletion</h3>
        <p>Delete this resource? This action cannot be undone.</p>
        <div className="modal-actions">
          <Button variant="secondary">Cancel</Button>
          <Button variant="danger">Delete</Button>
        </div>
      </>
    ),
  },
};

export const Closed: Story = {
  args: {
    ...Open.args,
    open: false,
  },
};

export const CloseDisabled: Story = {
  args: {
    ...Open.args,
    titleId: "modal-close-disabled-title",
    closeDisabled: true,
    children: (
      <>
        <h3 id="modal-close-disabled-title">Deleting…</h3>
        <p>Please wait while the resource is deleted.</p>
      </>
    ),
  },
};

export const Interactive: Story = {
  args: {
    open: false,
    onClose: () => {},
    titleId: "modal-interactive-title",
    children: null,
  },
  render: function InteractiveModal() {
    const [open, setOpen] = useState(false);
    return (
      <>
        <Button onClick={() => setOpen(true)}>Open modal</Button>
        <Modal
          open={open}
          onClose={() => setOpen(false)}
          titleId="modal-interactive-title"
        >
          <h3 id="modal-interactive-title">Confirm deletion</h3>
          <p>Delete this resource? This action cannot be undone.</p>
          <div className="modal-actions">
            <Button variant="secondary" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button variant="danger" onClick={() => setOpen(false)}>
              Delete
            </Button>
          </div>
        </Modal>
      </>
    );
  },
};
