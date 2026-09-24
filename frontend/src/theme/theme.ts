import { createTheme } from '@mantine/core';

export const nodeFlowTheme = createTheme({
  primaryColor: 'nodeflow',
  primaryShade: 6,
  colors: {
    nodeflow: [
      '#effcf1',
      '#d9f8de',
      '#b2efba',
      '#86e492',
      '#60d870',
      '#4bce5d',
      '#3fc552',
      '#31ad43',
      '#259538',
      '#157a2b',
    ],
  },
  fontFamily: 'Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
  headings: {
    fontFamily: 'Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
    fontWeight: '600',
  },
  defaultRadius: 'sm',
  radius: {
    xs: '4px',
    sm: '8px',
    md: '10px',
    lg: '12px',
    xl: '14px',
  },
  cursorType: 'pointer',
  respectReducedMotion: true,
  components: {
    Button: {
      defaultProps: { radius: 'sm' },
    },
    ActionIcon: {
      defaultProps: { radius: 'sm' },
    },
    TextInput: {
      defaultProps: { radius: 'sm' },
    },
    NumberInput: {
      defaultProps: { radius: 'sm' },
    },
    PasswordInput: {
      defaultProps: { radius: 'sm' },
    },
    Select: {
      defaultProps: { radius: 'sm' },
    },
    TagsInput: {
      defaultProps: { radius: 'sm' },
    },
    Textarea: {
      defaultProps: { radius: 'sm' },
    },
    SegmentedControl: {
      defaultProps: { radius: 'sm' },
    },
    Modal: {
      defaultProps: {
        radius: 'md',
        centered: true,
        closeButtonProps: { 'aria-label': 'Закрыть диалог' },
      },
    },
  },
});
