import {createContext, useContext} from 'react';

export const InteractionContext = createContext({
  hidden: false,
  onActivity: (): void => {},
});

export const useInteraction = () => useContext(InteractionContext);
