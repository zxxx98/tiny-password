import React, {useEffect, useRef, useState} from 'react';
import {sensitiveClipboard} from '../privacy/clipboard';
import {NewsprintButton} from './NewsprintButton';

interface CopyBlockButtonProps {
  label: string;
  value: string;
  accessibilityLabel?: string;
  testID?: string;
}

export function CopyBlockButton({
  label,
  value,
  accessibilityLabel,
  testID,
}: CopyBlockButtonProps): React.JSX.Element {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (timer.current !== null) {
        clearTimeout(timer.current);
      }
    };
  }, []);

  const copy = () => {
    sensitiveClipboard.copy(value);
    setCopied(true);
    if (timer.current !== null) {
      clearTimeout(timer.current);
    }
    timer.current = setTimeout(() => setCopied(false), 1500);
  };

  return (
    <NewsprintButton
      label={copied ? 'COPIED' : label}
      variant="secondary"
      onPress={copy}
      accessibilityLabel={accessibilityLabel ?? label}
      testID={testID}
    />
  );
}
