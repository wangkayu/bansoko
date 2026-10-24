// Component entry point for business code: import from here or from the
// existing ui wrappers, never from @base-ui/react directly.
export { Dialog, type DialogTier } from "./Dialog";
export {
  Button,
  Field,
  Input,
  PasswordInput,
  Select,
  SpinnerIcon,
  Tabs,
  TabsList,
  TabsPanel,
  TabsRoot,
  TagInput,
  Textarea,
  type InputProps,
  type SelectOption,
} from "./controls";
export { Checkbox, RadioGroup, Segmented, Switch, type ChoiceOption, type SegmentedTone } from "./toggles";
export { HelpTip } from "./HelpTip";
export { DropdownMenu, Popconfirm, Popover, Tooltip, type MenuEntry } from "./overlays";
export { Alert, Progress, type AlertTone } from "./feedback";
export { LAYER, LayerContext, POPUP_OFFSET, usePopupZIndex } from "./layers";
// Status (dot + label) and category chips.
export { StatePill, type StatePillTone } from "../StatePill";
export { Chip, type ChipTone } from "../Chip";
export { StateBlock, type StateBlockKind } from "../StateBlock";
